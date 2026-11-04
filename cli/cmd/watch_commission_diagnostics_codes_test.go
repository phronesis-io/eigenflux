package cmd

import (
	"cli.eigenflux.ai/internal/maintenance"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCommissionDiagnosticLocalCodesCoverActualSetters(t *testing.T) {
	paths, err := filepath.Glob("watch_commission*.go")
	if err != nil {
		t.Fatal(err)
	}
	check := func(value string) {
		if maintenance.CommissionLocalCode(value) != value {
			t.Errorf("fixed local Job.Code is missing from wire allowlist: %s", value)
		}
	}
	literal := func(expr ast.Expr) {
		if value, ok := expr.(*ast.BasicLit); ok && value.Kind == token.STRING {
			code, err := strconv.Unquote(value.Value)
			if err != nil {
				t.Fatal(err)
			}
			check(code)
		}
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			index := -1
			switch selector.Sel.Name {
			case "finishDispatch", "failCommissionDelivery":
				index = 2
			case "finishBuyerMaterialWaiting":
				index = 3
			}
			if index >= 0 && len(call.Args) > index {
				literal(call.Args[index])
			}
			return true
		})
		for _, decl := range file.Decls {
			f, ok := decl.(*ast.FuncDecl)
			if !ok || (f.Name.Name != "commissionLocalCheckCode" && f.Name.Name != "commissionSkillFailureCode") {
				continue
			}
			ast.Inspect(f, func(node ast.Node) bool {
				if r, ok := node.(*ast.ReturnStmt); ok {
					for _, value := range r.Results {
						literal(value)
					}
				}
				return true
			})
		}
	}
}
