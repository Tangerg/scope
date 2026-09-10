// Package arch holds architecture-fitness tests for the chatclient package.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chatclient"
)

func TestClientKeepsFocusedCallSurface(t *testing.T) {
	want := []string{"Call", "Output"}
	if methods := declaredMethods(t, "Client"); !slices.Equal(methods, want) {
		t.Fatalf("Client methods = %v, want %v", methods, want)
	}
	assertReceiverMethodsInFile(t, "Client", "client.go")
	if methods := declaredMethods(t, "StreamClient"); !slices.Equal(methods, []string{"Stream"}) {
		t.Fatalf("StreamClient methods = %v, want [Stream]", methods)
	}
	if methods := reflectedMethods(reflect.TypeFor[chatclient.OutputFormat[string]]()); len(methods) != 0 {
		t.Fatalf("OutputFormat methods = %v, want opaque format value", methods)
	}
}

func declaredMethods(t *testing.T, receiver string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var methods []string
	for _, path := range productionGoFiles(t) {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || !ast.IsExported(function.Name.Name) || len(function.Recv.List) != 1 {
				continue
			}
			if receiverName(function.Recv.List[0].Type) == receiver {
				methods = append(methods, function.Name.Name)
			}
		}
	}
	slices.Sort(methods)
	return methods
}

func assertReceiverMethodsInFile(t *testing.T, receiver, filename string) {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range productionGoFiles(t) {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || len(function.Recv.List) != 1 || receiverName(function.Recv.List[0].Type) != receiver {
				continue
			}
			if filepath.Base(path) != filename {
				t.Errorf("%s.%s is declared in %s, want %s", receiver, function.Name.Name, filepath.Base(path), filename)
			}
		}
	}
}

func receiverName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.StarExpr:
		return receiverName(expression.X)
	case *ast.IndexExpr:
		return receiverName(expression.X)
	case *ast.IndexListExpr:
		return receiverName(expression.X)
	default:
		return ""
	}
}

func reflectedMethods(typ reflect.Type) []string {
	methods := make([]string, 0, typ.NumMethod())
	for method := range typ.Methods() {
		methods = append(methods, method.Name)
	}
	slices.Sort(methods)
	return methods
}

func productionGoFiles(t *testing.T) []string {
	t.Helper()
	root := packageRoot(t)
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk module: %v", err)
	}
	return files
}

func packageRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
