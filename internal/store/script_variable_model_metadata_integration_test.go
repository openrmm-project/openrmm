package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/p1ngu/rmm/internal/config"
)

func TestScriptVariableModelQueryBoundsMetadataAndNeverSelectsValuesIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newIsolatedStore(t, "model_variable_metadata", url)
	ctx := context.Background()
	ordinary, err := st.CreateScriptVariable(ctx, "customerKey", strings.Repeat("語", ScriptVariableModelDescriptionRunes+1), false)
	if err != nil {
		t.Fatal(err)
	}
	const value = "synthetic-value-must-not-enter-model-metadata"
	if err := st.SetScriptVariableValue(ctx, &config.Config{}, ordinary.ID, "global", "", value); err != nil {
		t.Fatal(err)
	}
	oversized := "large_" + strings.Repeat("x", ScriptVariableModelNameBytes)
	if _, err := st.CreateScriptVariable(ctx, oversized, "SearchNeedle", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateScriptVariable(ctx, "searchKey", "SearchNeedle", false); err != nil {
		t.Fatal(err)
	}
	definitions, matched, err := st.ScriptVariableDefinitionsForModel(ctx, "")
	if err != nil || matched != 3 || len(definitions) != 2 {
		t.Fatalf("metadata selection: matched=%d rows=%d err=%v", matched, len(definitions), err)
	}
	for _, d := range definitions {
		if d.Name == "customerKey" && (!d.DescriptionTruncated || utf8.RuneCountInString(d.Description) != ScriptVariableModelDescriptionRunes) {
			t.Fatal("description was not clipped on the model projection")
		}
	}
	raw, _ := json.Marshal(definitions)
	if strings.Contains(string(raw), value) || strings.Contains(string(raw), "\"values\"") {
		t.Fatal("model projection contains values")
	}
	definitions, matched, err = st.ScriptVariableDefinitionsForModel(ctx, " SEARCHNEEDLE ")
	if err != nil || matched != 2 || len(definitions) != 1 || definitions[0].Name != "searchKey" {
		t.Fatal("search or oversized-name omission failed")
	}
	definitions, matched, err = st.ScriptVariableDefinitionsForModel(ctx, "searchKey SearchNeedle")
	if err != nil || matched != 1 || len(definitions) != 1 || definitions[0].Name != "searchKey" {
		t.Fatal("search across name/description boundary changed")
	}
	// Storage still preserves the user's full identifier and description.
	all, err := st.ListScriptVariables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range all {
		if d.Name == oversized {
			found = true
		}
	}
	if !found {
		t.Fatal("model budget changed stored fields")
	}
}
