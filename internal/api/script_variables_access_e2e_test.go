package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func listedVariable(t *testing.T, f *scriptFixture, name string) store.ScriptVariable {
	t.Helper()
	rec := f.do(http.MethodGet, "/api/script-variables", "")
	var out struct {
		Variables []store.ScriptVariable `json:"variables"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	for _, v := range out.Variables {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("%s not listed: %s", name, rec.Body.String())
	return store.ScriptVariable{}
}

func valueScopes(v store.ScriptVariable) map[string]string {
	out := map[string]string{}
	for _, value := range v.Values {
		out[value.ScopeType+":"+value.ScopeID] = value.Value
	}
	return out
}

// CI-only PostgreSQL, through the real router: who sees which values, who may
// define, change, remove or clear them, and that each change is audited by name.
func TestScriptVariableAccessThroughTheAPIE2E(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	clientA, err := f.store.CreateClient(ctx, "A "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := f.store.CreateClient(ctx, "B "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	name := "SITE_CODE_" + strings.ToUpper(uuid.NewString()[:8])
	rec := f.do(http.MethodPost, "/api/script-variables", `{"name":"`+name+`","description":"Site code","secret":false}`)
	var created struct{ ID string }
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	base := "/api/script-variables/" + created.ID
	for scope, value := range map[string]string{"global:": "G", "client:" + clientA.ID: "A", "client:" + clientB.ID: "B"} {
		parts := strings.SplitN(scope, ":", 2)
		body, _ := json.Marshal(map[string]string{"scope_type": parts[0], "scope_id": parts[1], "value": value})
		if rec := f.do(http.MethodPut, base+"/values", string(body)); rec.Code != http.StatusOK {
			t.Fatalf("set %s: %d %s", scope, rec.Code, rec.Body.String())
		}
	}
	if got := valueScopes(listedVariable(t, f, name)); len(got) != 3 || got["client:"+clientB.ID] != "B" {
		t.Fatalf("admin sees %v", got)
	}

	// Reading the library without the variables permission shows names, never values.
	restore := f.asUser(t, "", []string{models.PermScriptsView}, nil)
	if v := listedVariable(t, f, name); len(v.Values) != 0 {
		t.Fatalf("values shown without scripts.variables: %+v", v.Values)
	}
	restore()

	// Confined to client A: the global value and A's, never B's; no definition
	// changes, no global value, nothing of B's; A's own value can be cleared.
	restore = f.asUser(t, "", []string{models.PermScriptsView, models.PermScriptsVariables},
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: clientA.ID}})
	got := valueScopes(listedVariable(t, f, name))
	if len(got) != 2 || got["global:"] != "G" || got["client:"+clientA.ID] != "A" {
		t.Fatalf("scoped user sees %v", got)
	}
	clear := func(scopeType, scopeID string) int {
		q := url.Values{"scope_type": {scopeType}, "scope_id": {scopeID}}
		return f.do(http.MethodDelete, base+"/values?"+q.Encode(), "").Code
	}
	for what, code := range map[string]int{
		"create":       f.do(http.MethodPost, "/api/script-variables", `{"name":"SCOPED_NEW","secret":false}`).Code,
		"update":       f.do(http.MethodPut, base, `{"description":"x","secret":true}`).Code,
		"delete":       f.do(http.MethodDelete, base, "").Code,
		"clear global": clear("global", ""),
		"clear B":      clear("client", clientB.ID),
		"set B":        f.do(http.MethodPut, base+"/values", `{"scope_type":"client","scope_id":"`+clientB.ID+`","value":"x"}`).Code,
	} {
		if code != http.StatusForbidden {
			t.Errorf("scoped %s = %d, want 403", what, code)
		}
	}
	if code := clear("client", clientA.ID); code != http.StatusOK {
		t.Fatalf("scoped clear of its own client = %d", code)
	}
	restore()
	if e := f.audit("script_variable.clear"); e == nil || !strings.Contains(string(e.Detail), name) || !strings.Contains(string(e.Detail), clientA.ID) {
		t.Fatalf("clear audit: %+v", e)
	}
	got = valueScopes(listedVariable(t, f, name))
	if _, stillA := got["client:"+clientA.ID]; len(got) != 2 || stillA {
		t.Fatalf("after clearing A's value: %v", got)
	}

	// Definitions: a name is required; making it secret hides every value from
	// then on; removal is audited by name.
	if rec := f.do(http.MethodPost, "/api/script-variables", `{"name":"  ","secret":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name = %d", rec.Code)
	}
	if rec := f.do(http.MethodPut, base, `{"description":"Now secret","secret":true}`); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	// The audit detail is jsonb: compare its fields, not its spelling.
	var updated struct {
		Name      string `json:"name"`
		Secret    bool   `json:"secret"`
		WasSecret bool   `json:"was_secret"`
	}
	if e := f.audit("script_variable.update"); e == nil || json.Unmarshal(e.Detail, &updated) != nil || updated.Name != name || !updated.Secret || updated.WasSecret {
		t.Fatalf("update audit: %+v", e)
	}
	for _, value := range listedVariable(t, f, name).Values {
		if value.Value != "" || !value.Set {
			t.Fatalf("a secret value was returned: %+v", value)
		}
	}
	if rec := f.do(http.MethodDelete, base, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if e := f.audit("script_variable.delete"); e == nil || !strings.Contains(string(e.Detail), name) {
		t.Fatalf("delete audit: %+v", e)
	}
}

// Binding variables to a library script, or editing a script that has
// bindings, takes the variables permission even when the request does not
// mention variables.
func TestScriptVariableBindingsNeedTheirPermissionE2E(t *testing.T) {
	f := newScriptFixture(t)
	name := "VENDOR_KEY_" + strings.ToUpper(uuid.NewString()[:8])
	if rec := f.do(http.MethodPost, "/api/script-variables", `{"name":"`+name+`","secret":true}`); rec.Code != http.StatusCreated {
		t.Fatalf("variable: %d", rec.Code)
	}
	restore := f.asUser(t, "", []string{models.PermScriptsView, models.PermScriptsManage}, nil)
	bound := `{"name":"Installer","shell":"sh","body":"echo $` + name + `","variables":["` + name + `"]}`
	if rec := f.do(http.MethodPost, "/api/scripts", bound); rec.Code != http.StatusForbidden {
		t.Fatalf("binding without scripts.variables = %d", rec.Code)
	}
	rec := f.do(http.MethodPost, "/api/scripts", `{"name":"Plain","shell":"sh","body":"echo hi"}`)
	var plain models.Script
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &plain) != nil {
		t.Fatalf("plain script: %d %s", rec.Code, rec.Body.String())
	}
	restore()
	if rec := f.do(http.MethodPut, "/api/scripts/"+plain.ID, `{"name":"Plain","shell":"sh","body":"echo $`+name+`","variables":["`+name+`"]}`); rec.Code != http.StatusOK {
		t.Fatalf("admin binding: %d %s", rec.Code, rec.Body.String())
	}
	restore = f.asUser(t, "", []string{models.PermScriptsView, models.PermScriptsManage}, nil)
	if rec := f.do(http.MethodPut, "/api/scripts/"+plain.ID, `{"name":"Renamed","shell":"sh","body":"echo changed"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("editing a bound script without scripts.variables = %d", rec.Code)
	}
	restore()
	got, err := f.store.GetScript(context.Background(), plain.ID)
	if err != nil || got.Name != "Plain" || strings.Join(got.Variables, ",") != name {
		t.Fatalf("the refused edit changed the script: %+v %v", got, err)
	}
}
