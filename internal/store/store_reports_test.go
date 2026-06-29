package store

import (
	"strings"
	"testing"
)

func TestReportFilterWhere(t *testing.T) {
	tests := []struct {
		name      string
		f         ReportFilter
		wantArgs  []any
		wantEmpty bool
		contains  []string // substrings required in the WHERE clause
	}{
		{
			name:      "unrestricted, no filter",
			f:         ReportFilter{},
			wantEmpty: true,
		},
		{
			name:     "scope user only",
			f:        ReportFilter{ScopeUserID: "u1"},
			wantArgs: []any{"u1"},
			contains: []string{"user_scopes", "$1"},
		},
		{
			name:     "client filter only",
			f:        ReportFilter{ScopeType: "client", ScopeID: "c1"},
			wantArgs: []any{"c1"},
			contains: []string{"cl.id=$1"},
		},
		{
			name:     "site filter only",
			f:        ReportFilter{ScopeType: "site", ScopeID: "s1"},
			wantArgs: []any{"s1"},
			contains: []string{"a.site_id=$1"},
		},
		{
			name:     "group filter only",
			f:        ReportFilter{ScopeType: "group", ScopeID: "g1"},
			wantArgs: []any{"g1"},
			contains: []string{"a.group_id=$1"},
		},
		{
			// The security-critical case: scope AND filter, with correct $-numbering.
			name:     "scope user AND site filter are ANDed",
			f:        ReportFilter{ScopeUserID: "u1", ScopeType: "site", ScopeID: "s1"},
			wantArgs: []any{"u1", "s1"},
			contains: []string{"$1", "a.site_id=$2", " AND "},
		},
		{
			// An unknown scope_type must NOT add a filter (no silent wildcard).
			name:      "unknown scope type ignored",
			f:         ReportFilter{ScopeType: "bogus", ScopeID: "x"},
			wantEmpty: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			where, args := tc.f.where()
			if tc.wantEmpty {
				if where != "" {
					t.Fatalf("expected empty WHERE, got %q", where)
				}
				if len(args) != 0 {
					t.Fatalf("expected no args, got %v", args)
				}
				return
			}
			if !strings.HasPrefix(where, " WHERE ") {
				t.Fatalf("WHERE should start with ' WHERE ', got %q", where)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tc.wantArgs)
			}
			for i, a := range tc.wantArgs {
				if args[i] != a {
					t.Fatalf("args[%d] = %v, want %v", i, args[i], a)
				}
			}
			for _, sub := range tc.contains {
				if !strings.Contains(where, sub) {
					t.Fatalf("WHERE %q missing %q", where, sub)
				}
			}
		})
	}
}
