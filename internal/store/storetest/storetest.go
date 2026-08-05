// Package storetest gives DB-backed tests their own Postgres schema.
//
// It lives in its own package so tests in any package can use it — a helper
// defined in one package's _test.go file is invisible to every other. Nothing
// outside a test imports this, so it never reaches the server binary.
package storetest

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/p1ngu/rmm/internal/store"
)

// NewSchema returns a migrated store on a schema of its own, dropped on cleanup.
//
// Per-schema rather than per-database so DB-backed tests neither truncate shared
// tables nor collide when packages run in parallel. The prefix names the caller,
// which makes a leaked schema traceable to the test that left it.
//
// Callers skip on their own when RMM_TEST_DATABASE_URL is unset: there is
// nothing here to connect to, and a silent skip inside this helper would hide
// that a whole package never ran.
func NewSchema(t *testing.T, prefix, databaseURL string) *store.Store {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	database, err := url.Parse(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	query := database.Query()
	query.Set("search_path", schemaName)
	database.RawQuery = query.Encode()
	st, err := store.New(ctx, database.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}
