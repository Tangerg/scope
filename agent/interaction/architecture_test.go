package interaction

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDispatcherCannotOwnOrStartManagedProcesses(t *testing.T) {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "dispatcher.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]struct{}{
		"Engine": {}, "Process": {}, "StartChild": {}, "WaitForChildren": {},
	}
	ast.Inspect(file, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if _, blocked := forbidden[identifier.Name]; blocked {
			t.Errorf("dispatcher.go references forbidden managed-lifecycle identifier %q", identifier.Name)
		}
		return true
	})
}
