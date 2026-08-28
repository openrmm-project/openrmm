package store

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newIsolatedStore is storetest.NewSchema for tests that live *inside* package
// store. It cannot use that package: storetest imports store, so an in-package
// test importing storetest would be an import cycle. The logic is deliberately
// the same — a schema of its own, migrated, dropped on cleanup.
//
// It exists because the alternative these tests used was `TRUNCATE users,
// clients, ... CASCADE` on the shared schema. Go runs packages in parallel, so
// that truncate deletes whatever another package's tests are in the middle of
// using: at best a vanished fixture, at worst a deadlock against their inserts
// (Postgres reports the pair, an AccessExclusiveLock against a RowShareLock).
// CI never saw it because it sets no RMM_TEST_DATABASE_URL and these skip; it
// fired locally, where the suite is actually run against a database.
//
// Some of these tests genuinely need global state — BootstrapAdmin only succeeds
// when *no* user exists — which is exactly why they get a whole schema instead
// of a scoped delete.
func newIsolatedStore(t *testing.T, prefix, databaseURL string) *Store {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()

	st, err := New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}
