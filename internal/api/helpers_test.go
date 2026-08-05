package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
	"github.com/p1ngu/rmm/internal/store/storetest"
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

// newTestStore is storetest.NewSchema under this package's name, kept so the
// existing DB-backed tests here read unchanged.
//
// Note that CI runs the DB-backed packages in the "Database migrations"
// workflow — a test added here only ever runs if that workflow's package list
// and path filter include this package.
func newTestStore(t *testing.T, databaseURL string) *store.Store {
	t.Helper()
	return storetest.NewSchema(t, "api_test", databaseURL)
}
