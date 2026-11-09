package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestDisabledUserCannotUseOAuthCredentialsIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newIsolatedStore(t, "oauth_disabled", url)
	if err := st.CreateOAuthClient(ctx, "synthetic-client", "test", []string{"http://localhost/callback"}); err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		label := "enabled"
		if disabled {
			label = "disabled"
		}
		t.Run(label, func(t *testing.T) {
			u, err := st.CreateUser(ctx, label, "synthetic-password-hash", models.RoleAdmin)
			if err != nil {
				t.Fatal(err)
			}
			code, access, refresh := label+"-code", label+"-access", label+"-refresh"
			if err := st.CreateOAuthCode(ctx, code, "synthetic-client", u.ID, "http://localhost/callback", "challenge", "mcp", time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateOAuthToken(ctx, access, &refresh, "synthetic-client", u.ID, "mcp", time.Now().Add(time.Hour), time.Now().Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, _, ok, err := st.OAuthTokenPrincipal(ctx, access); err != nil || !ok {
				t.Fatalf("active prerequisite ok=%v err=%v", ok, err)
			}
			if disabled {
				if _, err := st.SetUserDisabled(ctx, u.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, ok, err := st.OAuthTokenPrincipal(ctx, access); err != nil || ok == disabled {
				t.Fatalf("access ok=%v err=%v", ok, err)
			}
			if _, _, _, ok, err := st.ConsumeOAuthRefresh(ctx, refresh); err != nil || ok == disabled {
				t.Fatalf("refresh ok=%v err=%v", ok, err)
			}
			if _, _, _, _, _, ok, err := st.ConsumeOAuthCode(ctx, code); err != nil || ok == disabled {
				t.Fatalf("code ok=%v err=%v", ok, err)
			}
			err = st.CreateOAuthToken(ctx, label+"-new", nil, "synthetic-client", u.ID, "mcp", time.Now().Add(time.Hour), time.Time{})
			if disabled && err != ErrNotFound {
				t.Fatalf("disabled token issuance: %v", err)
			}
			if !disabled && err != nil {
				t.Fatalf("enabled token issuance: %v", err)
			}
		})
	}
}
