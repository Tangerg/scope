package storetest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestFilterCorpusCoversEveryOperator(t *testing.T) {
	covered := make(map[filter.Operator]bool)
	names := make(map[string]bool)
	var collect func(filter.Expr)
	collect = func(expr filter.Expr) {
		switch node := expr.(type) {
		case *filter.BinaryExpr:
			covered[node.Operator()] = true
			collect(node.Left())
			collect(node.Right())
		case *filter.UnaryExpr:
			covered[node.Operator()] = true
			collect(node.Right())
		}
	}
	for _, test := range filterCases() {
		if test.name == "" || names[test.name] {
			t.Fatalf("corpus case name must be non-empty and unique: %q", test.name)
		}
		names[test.name] = true
		predicate, err := filter.Parse(test.source)
		if err != nil {
			t.Fatal(err)
		}
		collect(predicate)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "../filter/operator.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		decl, ok := declaration.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			continue
		}
		for _, specification := range decl.Specs {
			spec := specification.(*ast.ValueSpec)
			kind, ok := spec.Type.(*ast.Ident)
			if !ok || kind.Name != "Operator" {
				continue
			}
			value, err := strconv.Unquote(spec.Values[0].(*ast.BasicLit).Value)
			if err != nil {
				t.Fatal(err)
			}
			operator := filter.Operator(value)
			if !covered[operator] {
				t.Errorf("operator %s has no semantic conformance case", spec.Names[0].Name)
			}
			delete(covered, operator)
		}
	}
	if len(covered) != 0 {
		t.Errorf("corpus covers undeclared operators: %v", covered)
	}
}
