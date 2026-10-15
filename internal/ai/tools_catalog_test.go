package ai

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// handledNames reads the tool names Execute and toolPermission dispatch on,
// from the source rather than from a list kept beside it: a list here would
// drift exactly the way the catalog did.
func handledNames(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || (fn.Name.Name != "Execute" && fn.Name.Name != "toolPermission") {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sw, ok := n.(*ast.SwitchStmt)
				if !ok {
					return true
				}
				if tag, ok := sw.Tag.(*ast.Ident); !ok || tag.Name != "name" {
					return true
				}
				for _, stmt := range sw.Body.List {
					for _, expr := range stmt.(*ast.CaseClause).List {
						if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if name, err := strconv.Unquote(lit.Value); err == nil {
								names[name] = true
							}
						}
					}
				}
				return true
			})
		}
	}
	if len(names) < 10 {
		t.Fatalf("found only %d tool names in Execute/toolPermission; did the switch move?", len(names))
	}
	return names
}

// Every tool Execute can run must be in the catalog. A model, in the chat or
// over MCP, can only call a function the catalog declares, so a handler without
// a declaration is dead code that other tools' descriptions still point at:
// export_inventory was exactly that from 2026-08-31 to v0.6.1.
func TestEveryHandledToolIsAdvertised(t *testing.T) {
	advertised := map[string]bool{}
	for _, tool := range Tools() {
		advertised[tool.Function.Name] = true
	}
	for name := range handledNames(t) {
		if !advertised[name] {
			t.Errorf("Execute handles %q but Tools() does not declare it", name)
		}
	}
}

// The export is offered exactly where the report it completes is.
func TestExportInventoryFollowsReportsView(t *testing.T) {
	has := func(perms map[string]bool, name string) bool {
		for _, tool := range ToolsForPermissions(perms) {
			if tool.Function.Name == name {
				return true
			}
		}
		return false
	}
	with := map[string]bool{models.PermReportsView: true}
	if !has(with, "export_inventory") || !has(with, "inventory_report") {
		t.Fatal("reports.view must offer inventory_report and export_inventory")
	}
	without := map[string]bool{models.PermDevicesView: true}
	if has(without, "export_inventory") {
		t.Fatal("export_inventory offered without reports.view")
	}
}
