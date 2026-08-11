package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

func (f *scriptFixture) categories() []models.ScriptCategory {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/api/script-categories", "")
	if rec.Code != http.StatusOK {
		f.t.Fatalf("list categories status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Categories []models.ScriptCategory `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out.Categories
}

func (f *scriptFixture) script(name, category string) *models.Script {
	f.t.Helper()
	sc, err := f.store.CreateScript(context.Background(), &models.Script{
		Name: name, Category: category, Shell: models.ShellBash, Body: "echo hi",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return sc
}

// A category is only ever a name some scripts share, so listing them means
// counting. The console needs the list to offer what exists instead of asking
// for free text — which is how one category becomes two that differ by a
// keystroke.
func TestListScriptCategoriesCountsUsageIntegration(t *testing.T) {
	f := newScriptFixture(t)

	f.script("a", "Maintenance")
	f.script("b", "Maintenance")
	f.script("c", "Networking")
	f.script("d", "") // uncategorized scripts are not a category

	got := f.categories()
	if len(got) != 2 {
		t.Fatalf("categories = %+v, want two", got)
	}
	// Ordered by name, so the list reads the same on every load.
	if got[0].Name != "Maintenance" || got[0].Count != 2 {
		t.Errorf("first = %+v, want Maintenance with 2", got[0])
	}
	if got[1].Name != "Networking" || got[1].Count != 1 {
		t.Errorf("second = %+v, want Networking with 1", got[1])
	}
}

// Renaming rewrites every script filed under the old name. This is the repair
// for a typo that has already been saved.
func TestRenameScriptCategoryMovesEveryScriptIntegration(t *testing.T) {
	f := newScriptFixture(t)
	a := f.script("a", "Netwoking")
	b := f.script("b", "Netwoking")
	untouched := f.script("c", "Maintenance")

	rec := f.do(http.MethodPut, "/api/script-categories/Netwoking", `{"name":"Networking"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Name    string `json:"name"`
		Scripts int    `json:"scripts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "Networking" || out.Scripts != 2 {
		t.Errorf("response = %+v, want Networking across 2 scripts", out)
	}

	for _, id := range []string{a.ID, b.ID} {
		sc, err := f.store.GetScript(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if sc.Category != "Networking" {
			t.Errorf("script %s category = %q, want Networking", sc.Name, sc.Category)
		}
	}
	if sc, err := f.store.GetScript(context.Background(), untouched.ID); err != nil || sc.Category != "Maintenance" {
		t.Errorf("an unrelated script moved: %+v, err=%v", sc, err)
	}
	if entry := f.audit("script.category.rename"); entry == nil {
		t.Error("no script.category.rename audit entry")
	}
}

// Renaming onto a name already in use merges the two. It is the only way to undo
// a typo that split one category in half, so it must work — and be recorded.
func TestRenameScriptCategoryMergesIntoAnExistingOneIntegration(t *testing.T) {
	f := newScriptFixture(t)
	f.script("a", "Networking")
	f.script("b", "Netwoking")

	if rec := f.do(http.MethodPut, "/api/script-categories/Netwoking", `{"name":"Networking"}`); rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := f.categories()
	if len(got) != 1 || got[0].Name != "Networking" || got[0].Count != 2 {
		t.Fatalf("categories after the merge = %+v, want one Networking with 2", got)
	}
}

// A category can be named before any script is filed under it — the reason it is
// registered rather than inferred from the scripts.
func TestCreateScriptCategoryWithoutAnyScriptIntegration(t *testing.T) {
	f := newScriptFixture(t)

	if rec := f.do(http.MethodPost, "/api/script-categories", `{"name":"Backup"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := f.categories()
	if len(got) != 1 || got[0].Name != "Backup" || got[0].Count != 0 {
		t.Fatalf("categories = %+v, want Backup with no scripts", got)
	}
	if f.audit("script.category.create") == nil {
		t.Error("no script.category.create audit entry")
	}

	// Filing a script under it later just counts.
	f.script("a", "Backup")
	if got := f.categories(); len(got) != 1 || got[0].Count != 1 {
		t.Errorf("categories after filing = %+v, want Backup with 1", got)
	}
}

// A name already taken is a conflict — including when only a script carries it
// and the registry has not adopted it yet, which would otherwise "succeed" while
// changing nothing.
func TestCreateScriptCategoryRejectsDuplicatesIntegration(t *testing.T) {
	f := newScriptFixture(t)
	f.script("a", "Networking")

	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"in use by a script", `{"name":"Networking"}`, http.StatusConflict},
		{"differing only by case", `{"name":"networking"}`, http.StatusConflict},
		{"blank", `{"name":"   "}`, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if rec := f.do(http.MethodPost, "/api/script-categories", tt.body); rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
	if got := f.categories(); len(got) != 1 {
		t.Errorf("a rejected create added a category: %+v", got)
	}

	// Registering twice through the script editor path is silent, not an error.
	f.script("b", "Networking")
	if got := f.categories(); len(got) != 1 || got[0].Count != 2 {
		t.Errorf("categories = %+v, want one Networking with 2", got)
	}
}

// Removing a category returns its scripts to uncategorized. The scripts survive;
// deleting a category is a filing decision, not a way to delete work.
func TestDeleteScriptCategoryKeepsTheScriptsIntegration(t *testing.T) {
	f := newScriptFixture(t)
	kept := f.script("a", "Temporary")

	if rec := f.do(http.MethodDelete, "/api/script-categories/Temporary", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := f.categories(); len(got) != 0 {
		t.Errorf("categories = %+v, want none", got)
	}
	sc, err := f.store.GetScript(context.Background(), kept.ID)
	if err != nil {
		t.Fatalf("the script was deleted with its category: %v", err)
	}
	if sc.Category != "" {
		t.Errorf("category = %q, want empty", sc.Category)
	}
	if entry := f.audit("script.category.delete"); entry == nil {
		t.Error("no script.category.delete audit entry")
	}
}

func TestRenameScriptCategoryRejectsBadInputIntegration(t *testing.T) {
	f := newScriptFixture(t)
	f.script("a", "Maintenance")

	for _, tt := range []struct {
		name, path, body string
		want             int
	}{
		// Nothing carries the name, so there is no category to rename — answering
		// with a cheerful zero would hide a typo in the request itself.
		{"unknown category", "/api/script-categories/Nonexistent-" + uuid.NewString(), `{"name":"X"}`, http.StatusNotFound},
		{"same name", "/api/script-categories/Maintenance", `{"name":"Maintenance"}`, http.StatusBadRequest},
		{"blank name points at DELETE", "/api/script-categories/Maintenance", `{"name":""}`, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(http.MethodPut, tt.path, tt.body)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
	if got := f.categories(); len(got) != 1 || got[0].Name != "Maintenance" {
		t.Errorf("a rejected rename changed the categories: %+v", got)
	}

	// Surrounding whitespace is the operator's slip, not part of the name.
	if rec := f.do(http.MethodPut, "/api/script-categories/Maintenance", `{"name":"  Upkeep  "}`); rec.Code != http.StatusOK {
		t.Fatalf("padded name status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := f.categories(); len(got) != 1 || got[0].Name != "Upkeep" {
		t.Errorf("categories = %+v, want the trimmed Upkeep", got)
	}
}

// A category name is not a URL-safe token: "Remote access" has a space, and the
// console sends it encoded.
func TestScriptCategoryNamesSurviveTheURLIntegration(t *testing.T) {
	f := newScriptFixture(t)
	f.script("a", "Remote access")

	rec := f.do(http.MethodPut, "/api/script-categories/"+strings.ReplaceAll("Remote access", " ", "%20"), `{"name":"Remote & console"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := f.categories(); len(got) != 1 || got[0].Name != "Remote & console" {
		t.Errorf("categories = %+v, want the renamed one", got)
	}
}
