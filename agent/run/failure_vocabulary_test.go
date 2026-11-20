package run

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

func TestStableFailureVocabulary(t *testing.T) {
	codes := map[string]string{
		"runtime.resource.limit_exceeded":       failureRuntimeResourceLimitExceeded.Code(),
		"runtime.capability.denied":             failureRuntimeCapabilityDenied.Code(),
		"runtime.child.admission.rejected":      failureRuntimeChildAdmissionRejected.Code(),
		"runtime.child.request.canceled":        failureRuntimeChildRequestCanceled.Code(),
		"runtime.child.request.invalid":         failureRuntimeChildRequestInvalid.Code(),
		"runtime.counter.exhausted":             failureRuntimeCounterExhausted.Code(),
		"runtime.dispatch.failed":               failureRuntimeDispatchFailed.Code(),
		"runtime.dispatch.settlement.invalid":   failureRuntimeDispatchSettlementInvalid.Code(),
		"runtime.effect.recovery.invalid":       failureRuntimeEffectRecoveryInvalid.Code(),
		"runtime.process.snapshot.failed":       failureRuntimeProcessSnapshotFailed.Code(),
		"runtime.process.snapshot.unrestorable": failureRuntimeProcessSnapshotUnrestorable.Code(),
		"runtime.process.start.failed":          failureRuntimeProcessStartFailed.Code(),
		"execution.effect.invalid":              failureExecutionEffectInvalid.Code(),
		"execution.step.failed":                 failureExecutionStepFailed.Code(),
		"execution.transition.invalid":          failureExecutionTransitionInvalid.Code(),
	}
	seen := make(map[string]bool)
	for _, path := range frameworkProductionGoFiles(t) {
		if filepath.Dir(path) != "../run" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.VAR {
				continue
			}
			for _, spec := range group.Specs {
				value := spec.(*ast.ValueSpec)
				for index, name := range value.Names {
					call, ok := value.Values[index].(*ast.CallExpr)
					if !ok {
						continue
					}
					if constructor, named := call.Fun.(*ast.Ident); !named || constructor.Name != "NewFailureClassification" {
						continue
					}
					literal, ok := call.Args[1].(*ast.BasicLit)
					if !ok {
						t.Fatalf("%s must declare its wire value", name.Name)
					}
					wire, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					actual, present := codes[wire]
					if !present || actual != wire || seen[wire] {
						t.Errorf("uncovered, changed, or duplicated failure code %s = %q", name.Name, wire)
					}
					seen[wire] = true
				}
			}
		}
	}
	if len(seen) != len(codes) {
		t.Fatalf("declared codes = %d, covered codes = %d", len(seen), len(codes))
	}
}

func TestFailureConstructionUsesDeclaredVocabulary(t *testing.T) {
	for _, path := range frameworkProductionGoFiles(t) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		// Package-level declarations are the vocabulary; every construction
		// inside a function must name one of them.
		for _, declaration := range file.Decls {
			if body, ok := declaration.(*ast.FuncDecl); ok {
				inspectFailureConstruction(t, fset, file, body)
			}
		}
	}
}

func inspectFailureConstruction(t *testing.T, fset *token.FileSet, file *ast.File, body *ast.FuncDecl) {
	t.Helper()
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch function := call.Fun.(type) {
		case *ast.Ident:
			name = function.Name
		case *ast.SelectorExpr:
			name = function.Sel.Name
		}
		index := -1
		switch name {
		case "NewFailure", "NewFailureClassification", "recordFailure":
			index = 1
		case "fail":
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Name.Name != name {
					continue
				}
				parameterIndex := 0
				for _, field := range function.Type.Params.List {
					for _, parameter := range field.Names {
						if parameter.Name == "code" {
							index = parameterIndex
						}
						parameterIndex++
					}
				}
			}
		}
		if index < 0 || len(call.Args) <= index {
			return true
		}
		if _, literal := call.Args[index].(*ast.BasicLit); literal {
			t.Errorf("%s: failure codes must use the owning package's declared vocabulary", fset.Position(call.Pos()))
		}
		return true
	})
}
