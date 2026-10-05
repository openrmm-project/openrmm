package api

import (
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOIDCHandoffExchangeBindingExpiryReplayAndConcurrentUseIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	e.srv.cfg.PublicURL = e.url
	user, err := e.store.CreateUser(e.ctx, "handoff-user", "unused-hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	state := auth.NewToken()
	issue := func(ttl time.Duration) string {
		ticket := auth.NewToken()
		if err := e.store.CreateOIDCHandoff(e.ctx, auth.Hash(ticket), user.ID, auth.Hash(state), time.Now().Add(ttl)); err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	redeem := func(ticket, browser string) int {
		req, _ := http.NewRequest("POST", e.url+"/api/auth/oidc/exchange", strings.NewReader(`{"code":"`+ticket+`"}`))
		req.Header.Set("Origin", e.url)
		req.Header.Set("Content-Type", "application/json")
		if browser != "" {
			req.AddCookie(&http.Cookie{Name: "rmm_dev_oidc_login", Value: browser})
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return 0
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	ticket := issue(time.Minute)
	for _, browser := range []string{"", "another-browser"} {
		if got := redeem(ticket, browser); got != 401 {
			t.Fatalf("wrong browser: %d", got)
		}
	}
	if got := redeem(ticket, state); got != 200 {
		t.Fatalf("legitimate handoff: %d", got)
	}
	if got := redeem(ticket, state); got != 401 {
		t.Fatalf("replayed handoff: %d", got)
	}
	if got := redeem(issue(-time.Second), state); got != 401 {
		t.Fatalf("expired handoff: %d", got)
	}
	concurrent := issue(time.Minute)
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- redeem(concurrent, state) }()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[401] != 1 {
		t.Fatalf("concurrent redemption: %v", counts)
	}
	disabled := issue(time.Minute)
	if _, err := e.store.SetUserDisabled(e.ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := redeem(disabled, state); got != 401 {
		t.Fatalf("disabled user: %d", got)
	}
}
