package api

import (
	"errors"
	"net/http"
	"strings"
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

func TestPasswordProblem(t *testing.T) {
	tests := []struct {
		name, password, username string
		wantProblem              string // "" = accepted
	}{
		{"meets every rule", "Correct horse battery staple!", "admin", ""},
		{"symbol may be accented punctuation", "Umasenhalonga¿", "admin", ""},
		{"digits count as neither upper nor symbol", "Abcdefghijk1", "admin", "symbol"},
		{"too short even when complex", "Ab!2345", "admin", "at least 12"},
		{"no uppercase", "correct horse battery staple!", "admin", "uppercase"},
		{"no symbol", "CorrectHorseBattery", "admin", "symbol"},
		{"same as username", "Administrator!", "Administrator!", "username"},
		{"common password", "passwordpassword", "admin", "uppercase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := passwordProblem(tt.password, tt.username)
			if tt.wantProblem == "" {
				if got != "" {
					t.Fatalf("passwordProblem() = %q, want it accepted", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("password %q should have been rejected", tt.password)
			}
			if !strings.Contains(got, tt.wantProblem) {
				t.Errorf("problem = %q, want it to mention %q", got, tt.wantProblem)
			}
		})
	}
}

// The message names the specific failure. Restating the whole policy makes an
// operator re-read it and guess which part they missed.
func TestPasswordProblemNamesTheSpecificFailure(t *testing.T) {
	if got := passwordProblem("correcthorsebattery", "admin"); strings.Contains(got, "symbol") {
		t.Errorf("a password missing both should report the first missing rule, got %q", got)
	}
	if got := passwordProblem("Correcthorsebattery", "admin"); !strings.Contains(got, "symbol") {
		t.Errorf("only the symbol is missing here, got %q", got)
	}
}

// validPassword is the boolean face of the same rule; the two must not drift.
func TestValidPasswordAgreesWithPasswordProblem(t *testing.T) {
	for _, p := range []string{
		"Correct horse battery staple!", "short", "correcthorsebatterystaple",
		"CORRECTHORSEBATTERY", "Correcthorsebattery1",
	} {
		if validPassword(p, "admin") != (passwordProblem(p, "admin") == "") {
			t.Errorf("validPassword and passwordProblem disagree on %q", p)
		}
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
