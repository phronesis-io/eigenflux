package keys

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// The independent admin module cannot import the root package. Verify its
// blacklist invalidator still targets the exact key used by the pipeline.
func TestAdminBlacklistWireContract(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "../../../console/console_api/handler_gen/eigenflux/console/console_service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		decl, ok := n.(*ast.ValueSpec)
		if !ok || len(decl.Names) != 1 || decl.Names[0].Name != "blacklistCacheKey" {
			return true
		}
		literal, ok := decl.Values[0].(*ast.BasicLit)
		if !ok {
			t.Fatal("blacklist key must remain a named string constant")
		}
		actual, err := strconv.Unquote(literal.Value)
		if err != nil || actual != Blacklist {
			t.Fatalf("admin blacklist key drift: %q", actual)
		}
		found = true
		return false
	})
	if !found {
		t.Fatal("admin blacklist key contract not found")
	}
}
