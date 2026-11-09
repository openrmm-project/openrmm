package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

func TestRefusedAIReleaseIsAuditedIntegration(t *testing.T) {
	f := newScriptFixture(t)
	for _, body := range []string{"reboot # SYNTHETIC_BODY_CANARY", "printf status"} {
		sc, err := f.store.CreateScript(context.Background(), &models.Script{Name: "audit release", Shell: "sh", Body: body})
		if err != nil {
			t.Fatal(err)
		}
		res := f.do(http.MethodPost, "/api/scripts/"+sc.ID+"/ai-release", `{"release":true,"content_hash":"`+sc.ContentHash+`"}`)
		action := "script.ai_release"
		want := http.StatusOK
		if body != "printf status" {
			action = "script.ai_release.refused"
			want = http.StatusUnprocessableEntity
		}
		if res.Code != want {
			t.Fatalf("release status=%d want=%d", res.Code, want)
		}
		entry := f.audit(action)
		if entry == nil || entry.Target != sc.ID || entry.Actor == "" {
			t.Fatalf("missing %s audit", action)
		}
		if body != "printf status" && strings.Contains(string(entry.Detail), "SYNTHETIC_BODY_CANARY") {
			t.Fatal("script body reached refused audit")
		}
		current, err := f.store.GetScript(context.Background(), sc.ID)
		if err != nil || current.AIReleased != (body == "printf status") {
			t.Fatalf("release state=%v err=%v", current, err)
		}
		if !json.Valid(entry.Detail) {
			t.Fatal("invalid audit detail")
		}
	}
}

func TestAIReleaseApprovesOnlyTheViewedContentIntegration(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	sc, err := f.store.CreateScript(ctx, &models.Script{Name: "reviewed A", Shell: "sh", Body: "printf original"})
	if err != nil {
		t.Fatal(err)
	}
	hashA := sc.ContentHash
	sc.Body = "printf changed"
	if _, err := f.store.UpdateScript(ctx, sc); err != nil {
		t.Fatal(err)
	}
	path := "/api/scripts/" + sc.ID + "/ai-release"
	res := f.do(http.MethodPost, path, `{"release":true,"content_hash":"`+hashA+`"}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("stale review=%d", res.Code)
	}
	res = f.do(http.MethodPost, path, `{"release":true}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing review hash=%d", res.Code)
	}
	current, err := f.store.GetScript(ctx, sc.ID)
	if err != nil || current.AIReleased {
		t.Fatalf("stale review changed release=%+v err=%v", current, err)
	}
	res = f.do(http.MethodPost, path, `{"release":true,"content_hash":"`+current.ContentHash+`"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("current review control=%d", res.Code)
	}
	res = f.do(http.MethodPost, path, `{"release":false}`)
	if res.Code != http.StatusOK {
		t.Fatalf("withdrawal without hash control=%d", res.Code)
	}
}
