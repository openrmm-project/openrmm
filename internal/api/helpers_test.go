package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func TestOneOf(t *testing.T) {
	if !oneOf("native", "native", "meshcentral") {
		t.Error("oneOf should accept a listed value")
	}
	if oneOf("vnc", "native", "meshcentral") {
		t.Error("oneOf should reject an unlisted value")
	}
	if oneOf("", "native") {
		t.Error("oneOf should reject empty when not listed")
	}
}

func TestValidPassword(t *testing.T) {
	tests := []struct {
		name, password, username string
		want                     bool
	}{
		{"long passphrase", "correct horse battery staple", "admin", true},
		{"too short", "hunter2", "admin", false},
		{"common", "passwordpassword", "admin", false},
		{"same as username", "longadministrator", "LongAdministrator", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validPassword(tt.password, tt.username); got != tt.want {
				t.Fatalf("validPassword() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPermissionsSubsetPreventsPrivilegeEscalation(t *testing.T) {
	issuer := map[string]bool{
		models.PermAPITokensManage: true,
		models.PermMCPUse:          true,
	}
	if !permissionsSubset([]string{models.PermMCPUse}, issuer) {
		t.Fatal("a narrower token role should be allowed")
	}
	if permissionsSubset([]string{models.PermMCPUse, models.PermUsersManage}, issuer) {
		t.Fatal("a role with permissions absent from the issuer should be rejected")
	}
}

func TestHTTPStatusFor(t *testing.T) {
	if got := httpStatusFor(store.ErrNotFound); got != http.StatusNotFound {
		t.Errorf("not-found mapped to %d, want 404", got)
	}
	if got := httpStatusFor(errors.New("boom")); got != http.StatusBadRequest {
		t.Errorf("generic error mapped to %d, want 400", got)
	}
}

// newTestStore gives a test its own Postgres schema, migrated and dropped on
// cleanup. Per-schema rather than per-database so DB-backed tests in this
// package neither truncate shared tables nor collide when run together.
//
// Skips are the caller's job: without RMM_TEST_DATABASE_URL there is nothing to
// connect to. Note that CI runs the DB-backed packages in the "Database
// migrations" workflow — a test added here only ever runs if that workflow's
// package list and path filter include this package.
func newTestStore(t *testing.T, databaseURL string) *store.Store {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "api_authorization_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	return st
}
