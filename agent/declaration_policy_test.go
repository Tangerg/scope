package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Repository-wide receiver, locality, and import-name rules live in dev/repoarch.
// These guards cover the declaration hazards that recur inside Agent.
func TestAgentDeclarationsKeepOneMeaning(t *testing.T) {
	marker := regexp.MustCompile(`(?m)^\s*(?://|/\*|\*)\s*(TODO|FIXME|XXX|HACK)\b`)
	for _, path := range frameworkProductionGoFiles(t) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, comments := range file.Comments {
			for _, comment := range comments.List {
				if marker.MatchString(comment.Text) {
					t.Errorf("%s: unfinished work must not replace implementation", fset.Position(comment.Pos()))
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				if node.Recv != nil && node.Name.Name == "String" {
					ast.Inspect(node.Body, func(child ast.Node) bool {
						if value, ok := child.(*ast.BasicLit); ok && value.Kind == token.STRING && value.Value == `"invalid"` {
							t.Errorf("%s: enum String must use its package's invalidEnumName", fset.Position(value.Pos()))
						}
						return true
					})
				}
			case *ast.GenDecl:
				if node.Tok == token.CONST && mixesIotaWithUnrelatedConstants(node) {
					t.Errorf("%s: iota vocabulary and unrelated constants need separate declarations", fset.Position(node.Pos()))
				}
			case *ast.RangeStmt:
				if node.Tok != token.DEFINE {
					break
				}
				for _, position := range redundantRangeCopies(node) {
					t.Errorf("%s: range variables are already iteration-scoped", fset.Position(position))
				}
			}
			return true
		})
	}
}

func mixesIotaWithUnrelatedConstants(group *ast.GenDecl) bool {
	var usesIota bool
	ast.Inspect(group, func(node ast.Node) bool {
		if identifier, ok := node.(*ast.Ident); ok && identifier.Name == "iota" {
			usesIota = true
		}
		return true
	})
	if !usesIota {
		return false
	}
	var enumType string
	for index, specification := range group.Specs {
		spec := specification.(*ast.ValueSpec)
		if len(spec.Values) == 0 {
			continue
		}
		var name string
		if typ, ok := spec.Type.(*ast.Ident); ok {
			name = typ.Name
		}
		if index == 0 {
			enumType = name
		}
		if name != enumType {
			return true
		}
		if name == "" {
			var memberUsesIota bool
			ast.Inspect(spec, func(node ast.Node) bool {
				if identifier, ok := node.(*ast.Ident); ok && identifier.Name == "iota" {
					memberUsesIota = true
				}
				return true
			})
			if !memberUsesIota {
				return true
			}
		}
	}
	return false
}

func redundantRangeCopies(loop *ast.RangeStmt) []token.Pos {
	var positions []token.Pos
	ast.Inspect(loop.Body, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.RangeStmt, *ast.FuncLit:
			return false
		}
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != len(assignment.Rhs) {
			return true
		}
		for index, left := range assignment.Lhs {
			name, named := left.(*ast.Ident)
			right, copied := assignment.Rhs[index].(*ast.Ident)
			if !named || !copied || name.Name != right.Name {
				continue
			}
			for _, variable := range []ast.Expr{loop.Key, loop.Value} {
				binding, ok := variable.(*ast.Ident)
				if ok && binding.Obj != nil && binding.Obj == right.Obj {
					positions = append(positions, assignment.Pos())
				}
			}
		}
		return true
	})
	return positions
}

func TestRangeCopyGuardRespectsLexicalScopes(t *testing.T) {
	for _, sample := range []struct {
		name, body string
		want       int
	}{
		{"direct", "value := value; _ = value", 1},
		{"if", "if true { value := value; _ = value }", 1},
		{"switch", "switch { case true: value := value; _ = value }", 1},
		{"select", "select { default: value := value; _ = value }", 1},
		{"shadow", "if value := 1; value > 0 { value := value; _ = value }", 0},
		{"nested range", "for _, value := range []int{1} { value := value; _ = value }", 0},
		{"closure", "_ = func() { value := value; _ = value }", 0},
	} {
		t.Run(sample.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "sample.go", "package sample; func f() { for _, value := range []int{1} { "+sample.body+" } }", 0)
			if err != nil {
				t.Fatal(err)
			}
			loop := file.Decls[0].(*ast.FuncDecl).Body.List[0].(*ast.RangeStmt)
			if got := len(redundantRangeCopies(loop)); got != sample.want {
				t.Fatalf("copies = %d, want %d", got, sample.want)
			}
		})
	}
}

// Closed enums use a full type prefix and an invalid zero value first. Valid
// uses explicit switch membership; comments describe semantics when names alone
// do not, rather than repeating every constant's spelling.
func TestEnumDeclarationsOwnTheirVocabulary(t *testing.T) {
	fset := token.NewFileSet()
	files := make([]*ast.File, 0)
	enums := make(map[string]bool)
	for _, path := range frameworkProductionGoFiles(t) {
		if filepath.Dir(path) != "." {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.TYPE {
				continue
			}
			for _, spec := range group.Specs {
				typ := spec.(*ast.TypeSpec)
				underlying, ok := typ.Type.(*ast.Ident)
				if ok && underlying.Name == "string" && typ.Name.IsExported() {
					enums[typ.Name.Name] = false
				}
			}
		}
	}
	for _, file := range files {
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				if declaration.Tok != token.CONST {
					continue
				}
				var current string
				for _, spec := range declaration.Specs {
					value := spec.(*ast.ValueSpec)
					if typ, ok := value.Type.(*ast.Ident); ok {
						current = typ.Name
					}
					seen, enum := enums[current]
					if !enum {
						continue
					}
					for _, name := range value.Names {
						if !strings.HasPrefix(name.Name, current) {
							t.Errorf("%s: enum member %s must begin with %s", fset.Position(name.Pos()), name.Name, current)
						}
						if !seen && name.Name != current+"Invalid" {
							t.Errorf("%s: %s must declare Invalid first", fset.Position(name.Pos()), current)
						}
						seen = true
						enums[current] = true
					}
				}
			case *ast.FuncDecl:
				if declaration.Recv == nil {
					continue
				}
				typ, ok := declaration.Recv.List[0].Type.(*ast.Ident)
				if !ok {
					continue
				}
				if _, enum := enums[typ.Name]; !enum {
					continue
				}
				switch declaration.Name.Name {
				case "Valid":
					if len(declaration.Body.List) != 1 {
						t.Errorf("%s: enum membership must be one explicit switch", fset.Position(declaration.Pos()))
						continue
					}
					if _, ok := declaration.Body.List[0].(*ast.SwitchStmt); !ok {
						t.Errorf("%s: enum membership must be one explicit switch", fset.Position(declaration.Pos()))
					}
				case "String":
					shared := false
					ast.Inspect(declaration.Body, func(node ast.Node) bool {
						if name, ok := node.(*ast.Ident); ok && name.Name == "invalidEnumName" {
							shared = true
						}
						return true
					})
					if !shared {
						t.Errorf("%s: enum String must use invalidEnumName", fset.Position(declaration.Pos()))
					}
				}
			}
		}
	}
}
