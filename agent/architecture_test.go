package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimeInspectionHasOnePublicOwner(t *testing.T) {
	process := reflect.TypeFor[*Process]()
	var methods []string
	for index := range process.NumMethod() {
		methods = append(methods, process.Method(index).Name)
	}
	want := []string{
		"Await", "Budget", "Capabilities", "DeliverSignals", "DeploymentRef", "ID", "Join",
		"Kill", "Pause", "Relation", "RequestCancellation", "ResolveUnknownEffect", "Resume", "StartedAt",
	}
	if !slices.Equal(methods, want) {
		t.Fatalf("Process must expose identity, control, and Await; runtime reads belong to Engine.InspectTree: %v", methods)
	}
	engine := reflect.TypeFor[*Engine]()
	var inspectionMethods []string
	for index := range engine.NumMethod() {
		method := engine.Method(index)
		if method.Type.NumOut() > 0 && method.Type.Out(0) == reflect.TypeFor[TreeInspection]() {
			inspectionMethods = append(inspectionMethods, method.Name)
		}
	}
	if !slices.Equal(inspectionMethods, []string{"InspectTree"}) {
		t.Fatalf("tree inspection must have one Engine entry: %v", inspectionMethods)
	}
}

func TestAdmissionAndObservationFactsAreImmutable(t *testing.T) {
	for _, fact := range []reflect.Type{
		reflect.TypeFor[ProcessAdmission](),
		reflect.TypeFor[ProcessStartOutcome](),
		reflect.TypeFor[Event](),
	} {
		t.Run(fact.Name(), func(t *testing.T) {
			for index := range fact.NumField() {
				if field := fact.Field(index); field.IsExported() {
					t.Errorf("immutable boundary fact exposes mutable field %s", field.Name)
				}
			}
		})
	}
}

func TestBoundaryValuesDoNotCarryRuntimeAuthority(t *testing.T) {
	for _, value := range []reflect.Type{
		reflect.TypeFor[ProcessAdmission](),
		reflect.TypeFor[ProcessStartOutcome](),
		reflect.TypeFor[Event](),
		reflect.TypeFor[TreeInspection](),
		reflect.TypeFor[ProcessInspection](),
	} {
		t.Run(value.Name(), func(t *testing.T) {
			assertNoRuntimeAuthority(t, value, make(map[reflect.Type]bool))
		})
	}
}

func assertNoRuntimeAuthority(t *testing.T, value reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	if seen[value] {
		return
	}
	seen[value] = true
	switch value {
	case reflect.TypeFor[Engine](), reflect.TypeFor[Process](), reflect.TypeFor[Deployment](),
		reflect.TypeFor[Definition](), reflect.TypeFor[Execution](), reflect.TypeFor[Dispatcher](),
		reflect.TypeFor[DeploymentResolver](), reflect.TypeFor[TreeDurability]():
		t.Errorf("boundary value carries runtime authority through %v", value)
		return
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		assertNoRuntimeAuthority(t, value.Elem(), seen)
	case reflect.Map:
		assertNoRuntimeAuthority(t, value.Key(), seen)
		assertNoRuntimeAuthority(t, value.Elem(), seen)
	case reflect.Struct:
		for index := range value.NumField() {
			assertNoRuntimeAuthority(t, value.Field(index).Type, seen)
		}
	case reflect.Func:
		for index := range value.NumIn() {
			assertNoRuntimeAuthority(t, value.In(index), seen)
		}
		for index := range value.NumOut() {
			assertNoRuntimeAuthority(t, value.Out(index), seen)
		}
	}
	if value.PkgPath() == reflect.TypeFor[Event]().PkgPath() {
		methods := value
		if value.Kind() != reflect.Interface {
			methods = reflect.PointerTo(value)
		}
		for index := range methods.NumMethod() {
			assertNoRuntimeAuthority(t, methods.Method(index).Type, seen)
		}
	}
}

func TestFrameworkRootExcludesHostAbstractions(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbiddenIdentifiers := map[string]bool{
		"Store": true, "Repository": true, "Transaction": true, "Lease": true,
	}
	forbiddenFragments := []string{"Session", "Conversation", "Workspace", "WriteSet"}
	files := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			for _, identifier := range declaredIdentifiers(node) {
				forbidden := forbiddenIdentifiers[identifier.Name]
				for _, fragment := range forbiddenFragments {
					forbidden = forbidden || strings.Contains(identifier.Name, fragment)
				}
				if forbidden {
					t.Errorf("%s declares forbidden Host abstraction identifier %q", name, identifier.Name)
				}
			}
			return true
		})
	}
}

func declaredIdentifiers(node ast.Node) []*ast.Ident {
	switch declaration := node.(type) {
	case *ast.TypeSpec:
		return []*ast.Ident{declaration.Name}
	case *ast.FuncDecl:
		return []*ast.Ident{declaration.Name}
	case *ast.ValueSpec:
		return declaration.Names
	case *ast.Field:
		return declaration.Names
	default:
		return nil
	}
}

func TestProcessConstructionRemainsEngineOwned(t *testing.T) {
	files := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				assertProcessFieldsArePrivate(t, name, declaration)
			case *ast.FuncDecl:
				if returnsProcessPointer(declaration.Type.Results) &&
					(declaration.Recv == nil || receiverTypeName(declaration.Recv) != "Engine") {
					t.Errorf("%s exports non-Engine Process construction through %s", name, declaration.Name.Name)
				}
			}
		}
	}
}

func TestTimeSleepIsScopedToSynctestCallback(t *testing.T) {
	files := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(files, filepath.Clean(path), nil, 0)
		if err != nil {
			return err
		}
		timePackage := importedPackageName(file, "time")
		if timePackage == "" {
			return nil
		}
		synctestPackage := importedPackageName(file, "testing/synctest")
		fakeClockSleeps := make(map[token.Pos]struct{})
		if synctestPackage != "" {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || !isPackageCall(call, synctestPackage, "Test") || len(call.Args) != 2 {
					return true
				}
				ast.Inspect(call.Args[1], func(callbackNode ast.Node) bool {
					callbackCall, ok := callbackNode.(*ast.CallExpr)
					if ok && isPackageCall(callbackCall, timePackage, "Sleep") {
						fakeClockSleeps[callbackCall.Pos()] = struct{}{}
					}
					return true
				})
				return true
			})
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isPackageCall(call, timePackage, "Sleep") {
				return true
			}
			if _, allowed := fakeClockSleeps[call.Pos()]; !allowed {
				position := files.Position(call.Pos())
				t.Errorf("%s uses time.Sleep outside synctest.Test; use a channel or Process barrier", position)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func importedPackageName(file *ast.File, path string) string {
	for _, specification := range file.Imports {
		importPath, err := strconv.Unquote(specification.Path.Value)
		if err != nil || importPath != path {
			continue
		}
		if specification.Name != nil {
			return specification.Name.Name
		}
		return filepath.Base(path)
	}
	return ""
}

func isPackageCall(call *ast.CallExpr, packageName, functionName string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != functionName {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == packageName
}

func assertProcessFieldsArePrivate(t *testing.T, filename string, declaration *ast.GenDecl) {
	t.Helper()
	for _, specification := range declaration.Specs {
		typeSpec, ok := specification.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != "Process" {
			continue
		}
		structure, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			t.Fatalf("%s declares Process as a non-struct", filename)
		}
		for _, field := range structure.Fields.List {
			for _, name := range field.Names {
				if name.IsExported() {
					t.Errorf("%s exposes mutable Process field %s", filename, name.Name)
				}
			}
		}
	}
}

func returnsProcessPointer(results *ast.FieldList) bool {
	if results == nil {
		return false
	}
	for _, result := range results.List {
		pointer, ok := result.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		identifier, ok := pointer.X.(*ast.Ident)
		if ok && identifier.Name == "Process" {
			return true
		}
	}
	return false
}

func receiverTypeName(receiver *ast.FieldList) string {
	if receiver == nil || len(receiver.List) != 1 {
		return ""
	}
	value := receiver.List[0].Type
	if pointer, ok := value.(*ast.StarExpr); ok {
		value = pointer.X
	}
	identifier, _ := value.(*ast.Ident)
	if identifier == nil {
		return ""
	}
	return identifier.Name
}
