package store

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareInventory(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"normal snapshot", `{"hostname":"pc","disks":[],"software":null}`, `{"hostname":"pc","disks":[],"software":null}`},
		{"Windows strings", `{"hostname":"pc","cpu_model":"CPU\u0000","hardware":{"serial_number":"SN\u0000123"},"network_interfaces":[{"name":"Ethernet\u0000"}],"software":[{"name":"App\u0000","manufacturer":"Corp\u0000"}]}`, `{"hostname":"pc","cpu_model":"CPU","hardware":{"serial_number":"SN123"},"network_interfaces":[{"name":"Ethernet"}],"software":[{"name":"App","manufacturer":"Corp"}]}`},
		{"array strings", `{"values":["a\u0000b",["c\u0000d"],null,true,2]}`, `{"values":["ab",["cd"],null,true,2]}`},
		{"literal escape and path", `{"path":"C:\\u0000\\app","literal":"\\u0000","\\u0000":"field"}`, `{"path":"C:\\u0000\\app","literal":"\\u0000","\\u0000":"field"}`},
		{"Unicode and controls", `{"name":"한국어 Русский Português 😀\t\n"}`, `{"name":"한국어 Русский Português 😀\t\n"}`},
		{"integer precision", `{"size":18446744073709551615,"count":9007199254740993,"percent":12.75}`, `{"size":18446744073709551615,"count":9007199254740993,"percent":12.75}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := prepareInventory([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			decode := func(raw []byte) any {
				t.Helper()
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.UseNumber()
				var v any
				if err := dec.Decode(&v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			if !reflect.DeepEqual(decode(got), decode([]byte(tc.want))) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPrepareInventoryRejectsExcessiveStructure(t *testing.T) {
	for name, raw := range map[string]string{
		"deep":  strings.Repeat(`{"n":`, 1000) + `0` + strings.Repeat(`}`, 1000),
		"broad": `{"values":[` + strings.Repeat(`{},`, 30000) + `{}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareInventory([]byte(raw)); err == nil {
				t.Fatal("excessive inventory structure accepted")
			}
		})
	}
}

func TestPrepareInventoryStructureLimitsPreserveNormalFleetData(t *testing.T) {
	// The reference agent caps installed software at 600 entries. The budget
	// must keep that legitimate maximum, including strings with JSON syntax.
	entry := `{"name":"App { [ \\\"quoted\\\" ] }","version":"1.2","manufacturer":"Example"}`
	raw := `{"hostname":"pc","software":[` + strings.Repeat(entry+`,`, 599) + entry + `]}`
	if _, err := prepareInventory([]byte(raw)); err != nil {
		t.Fatalf("normal software inventory rejected: %v", err)
	}
	// Counts are inclusive: nesting and token totals exactly at the limits
	// work, while one additional level or value must fail before conversion.
	depth := `{"values":` + strings.Repeat(`[`, maxInventoryDepth-1) + `0` + strings.Repeat(`]`, maxInventoryDepth-1) + `}`
	if _, err := prepareInventory([]byte(depth)); err != nil {
		t.Fatalf("allowed nesting rejected: %v", err)
	}
	tooDeep := `{"values":` + strings.Repeat(`[`, maxInventoryDepth) + `0` + strings.Repeat(`]`, maxInventoryDepth) + `}`
	if _, err := prepareInventory([]byte(tooDeep)); err == nil {
		t.Fatal("nesting beyond the limit accepted")
	}
	// Root object: 2 tokens, field name: 1, array delimiters: 2.
	values := maxInventoryJSONTokens - 5
	atLimit := `{"values":[` + strings.Repeat(`0,`, values-1) + `0]}`
	if _, err := prepareInventory([]byte(atLimit)); err != nil {
		t.Fatalf("allowed token count rejected: %v", err)
	}
	overLimit := `{"values":[` + strings.Repeat(`0,`, values) + `0]}`
	if _, err := prepareInventory([]byte(overLimit)); err == nil {
		t.Fatal("token count beyond the limit accepted")
	}
}

func TestPrepareInventorySizeLimitBeforeParsing(t *testing.T) {
	raw := []byte(strings.Repeat(" ", maxInventoryBytes+1))
	if _, err := prepareInventory(raw); err == nil || err.Error() != "inventory exceeds size limit" {
		t.Fatalf("oversized inventory must be refused before parsing: %v", err)
	}
}

func TestPrepareInventoryRejectsInvalidSnapshots(t *testing.T) {
	for _, raw := range []string{"", "not json", "null", "[]", "true", "42", `"text"`, `{} {}`, `{"bad\u0000key":1}`, `{"nested":[{"bad\u0000key":2}]}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := prepareInventory([]byte(raw)); err == nil {
				t.Fatal("invalid inventory accepted")
			}
		})
	}
}

func FuzzPrepareInventory(f *testing.F) {
	for _, seed := range []string{
		`{"hostname":"pc","software":[{"name":"App\u0000"}],"size":18446744073709551615}`,
		`{"path":"C:\\u0000\\app","text":"\\\"[{}]\\\""}`,
		`{"name":"Русский 한국어 😀"}`, `{"a":1,"a":2}`, `null`, `{} {}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out, err := prepareInventory(raw)
		if err != nil {
			return
		}
		if !json.Valid(out) {
			t.Fatal("accepted inventory did not produce valid JSON")
		}
		// Normalization must stabilize, including duplicate keys and literal
		// backslashes. A second pass must not silently change endpoint data.
		again, err := prepareInventory(out)
		if err != nil || !bytes.Equal(out, again) {
			t.Fatalf("normalization was not idempotent: %v", err)
		}
	})
}
