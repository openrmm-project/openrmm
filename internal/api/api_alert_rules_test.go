package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/store"
)

func TestAlertRuleNotificationSelectionUnit(t *testing.T) {
	a, b, outside := uuid.NewString(), uuid.NewString(), uuid.NewString()
	choices := []notificationRuleChoice{{ID: a}, {ID: b}}
	valid := store.NotificationRuleFilter{Mode: "selected", CheckIDs: []string{b, a, b}}
	if err := validateNotificationRules(&valid, choices); err != nil {
		t.Fatal(err)
	}
	if len(valid.CheckIDs) != 2 {
		t.Fatal("duplicates were not removed")
	}
	for _, tc := range []store.NotificationRuleFilter{
		{Mode: "unknown"},
		{Mode: "selected", CheckIDs: []string{outside}},
		{Mode: "all", CheckIDs: []string{"not-a-uuid"}},
		{Mode: "selected", CheckIDs: make([]string, 501)},
	} {
		if err := validateNotificationRules(&tc, choices); err == nil {
			t.Fatalf("bad preference accepted: %+v", tc)
		}
	}
	for _, mode := range []string{"all", "selected"} {
		f := store.NotificationRuleFilter{Mode: mode}
		if err := validateNotificationRules(&f, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAlertRuleFleetWritesRejectRestrictedPrincipalsUnit(t *testing.T) {
	s := &Server{} // a refusal must happen before any store call
	for name, handler := range map[string]http.HandlerFunc{"default": s.updateAlertDefault, "overrides": s.checkOverrides} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/", nil)
			req = req.WithContext(context.WithValue(req.Context(), scopeUserKey, "restricted-user"))
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d", rec.Code)
			}
		})
	}
}
