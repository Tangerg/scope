package run

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStableFailureVocabulary(t *testing.T) {
	codes := map[string]string{
		"runtime.resource.limit_exceeded":       failureCodeRuntimeResourceLimitExceeded,
		"runtime.capability.denied":             failureCodeRuntimeCapabilityDenied,
		"runtime.child.admission.rejected":      failureCodeRuntimeChildAdmissionRejected,
		"runtime.child.request.canceled":        failureCodeRuntimeChildRequestCanceled,
		"runtime.child.request.invalid":         failureCodeRuntimeChildRequestInvalid,
		"runtime.counter.exhausted":             failureCodeRuntimeCounterExhausted,
		"runtime.dispatch.failed":               failureCodeRuntimeDispatchFailed,
		"runtime.dispatch.panicked":             failureCodeRuntimeDispatchPanicked,
		"runtime.dispatch.settlement.invalid":   failureCodeRuntimeDispatchSettlementInvalid,
		"runtime.effect.recovery.invalid":       failureCodeRuntimeEffectRecoveryInvalid,
		"runtime.process.snapshot.failed":       failureCodeRuntimeProcessSnapshotFailed,
		"runtime.process.snapshot.unrestorable": failureCodeRuntimeProcessSnapshotUnrestorable,
		"runtime.process.start.failed":          failureCodeRuntimeProcessStartFailed,
		"execution.effect.invalid":              failureCodeExecutionEffectInvalid,
		"execution.step.failed":                 failureCodeExecutionStepFailed,
		"execution.transition.invalid":          failureCodeExecutionTransitionInvalid,
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
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, spec := range group.Specs {
				value := spec.(*ast.ValueSpec)
				for index, name := range value.Names {
					if !strings.HasPrefix(name.Name, "failureCode") {
						continue
					}
					literal, ok := value.Values[index].(*ast.BasicLit)
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
		ast.Inspect(file, func(node ast.Node) bool {
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
			case "NewFailure", "newRuntimeFailure", "recordFailure":
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
}
