package ai

import (
	"encoding/json"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// propose_check offered "disk_space" and "cpu_load", names models.ValidateCheck
// never accepted, so the assistant could not propose a disk or CPU check at all.
// Every type the schema offers must be one the server takes.
func TestProposeCheckOffersOnlySupportedTypes(t *testing.T) {
	var schema struct {
		Properties struct {
			Type struct {
				Enum []string `json:"enum"`
			} `json:"type"`
		} `json:"properties"`
	}
	found := false
	for _, tool := range Tools() {
		if tool.Function.Name != "propose_check" {
			continue
		}
		found = true
		if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("propose_check is not in the catalog")
	}
	if len(schema.Properties.Type.Enum) == 0 {
		t.Fatal("propose_check offers no check types")
	}
	for _, typ := range schema.Properties.Type.Enum {
		if !models.SupportedCheckType(typ) {
			t.Errorf("propose_check offers %q, which the server refuses", typ)
		}
	}
	for legacy, canonical := range legacyCheckTypes {
		if !models.SupportedCheckType(canonical) {
			t.Errorf("legacy %q maps to unsupported %q", legacy, canonical)
		}
	}
}
