package store

import (
	"bytes"
	"encoding/json"
	"reflect"
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

func TestPrepareInventoryRejectsInvalidSnapshots(t *testing.T) {
	for _, raw := range []string{"", "not json", "null", "[]", "true", "42", `"text"`, `{} {}`, `{"bad\u0000key":1}`, `{"nested":[{"bad\u0000key":2}]}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := prepareInventory([]byte(raw)); err == nil {
				t.Fatal("invalid inventory accepted")
			}
		})
	}
}
