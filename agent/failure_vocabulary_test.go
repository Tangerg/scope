package agent

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
		"engine.capability.denied":                           failureCodeEngineCapabilityDenied,
		"engine.child.admission.rejected":                    failureCodeEngineChildAdmissionRejected,
		"engine.child.budget_exhausted":                      failureCodeEngineChildBudgetExhausted,
		"engine.child.capability_escalation":                 failureCodeEngineChildCapabilityEscalation,
		"engine.child.control.invalid":                       failureCodeEngineChildControlInvalid,
		"engine.child.control.not_owned":                     failureCodeEngineChildControlNotOwned,
		"engine.child.control.settlement.invalid":            failureCodeEngineChildControlSettlementInvalid,
		"engine.child.deployment_unavailable":                failureCodeEngineChildDeploymentUnavailable,
		"engine.child.identity_conflict":                     failureCodeEngineChildIdentityConflict,
		"engine.child.initialization_outcome.unacknowledged": failureCodeEngineChildInitializationOutcomeUnacknowledged,
		"engine.child.input.invalid":                         failureCodeEngineChildInputInvalid,
		"engine.child.request.invalid":                       failureCodeEngineChildRequestInvalid,
		"engine.child.settlement.invalid":                    failureCodeEngineChildSettlementInvalid,
		"engine.child.signal.rejected":                       failureCodeEngineChildSignalRejected,
		"engine.child.start.interrupted":                     failureCodeEngineChildStartInterrupted,
		"engine.child.start.unavailable":                     failureCodeEngineChildStartUnavailable,
		"engine.child.tree_limit":                            failureCodeEngineChildTreeLimit,
		"engine.child.wait.satisfaction.encoding_failed":     failureCodeEngineChildWaitSatisfactionEncodingFailed,
		"engine.child.wait.satisfaction.invalid":             failureCodeEngineChildWaitSatisfactionInvalid,
		"engine.committed_execution_state.invalid":           failureCodeEngineCommittedExecutionStateInvalid,
		"engine.counter.exhausted":                           failureCodeEngineCounterExhausted,
		"engine.dispatch.canceled":                           failureCodeEngineDispatchCanceled,
		"engine.dispatch.deadline":                           failureCodeEngineDispatchDeadline,
		"engine.dispatch.failed":                             failureCodeEngineDispatchFailed,
		"engine.dispatch.panicked":                           failureCodeEngineDispatchPanicked,
		"engine.dispatch.settlement.invalid":                 failureCodeEngineDispatchSettlementInvalid,
		"engine.effect.phase.invalid":                        failureCodeEngineEffectPhaseInvalid,
		"engine.effect.recovery.invalid":                     failureCodeEngineEffectRecoveryInvalid,
		"engine.effect.settlement.invalid":                   failureCodeEngineEffectSettlementInvalid,
		"engine.finalize.invalid":                            failureCodeEngineFinalizeInvalid,
		"engine.framework_effect.settlement.invalid":         failureCodeEngineFrameworkEffectSettlementInvalid,
		"engine.limit.child_wait_signal":                     failureCodeEngineLimitChildWaitSignal,
		"engine.limit.effects":                               failureCodeEngineLimitEffects,
		"engine.limit.signals":                               failureCodeEngineLimitSignals,
		"engine.limit.snapshot":                              failureCodeEngineLimitSnapshot,
		"engine.limit.steps":                                 failureCodeEngineLimitSteps,
		"engine.process.attempt_exhausted":                   failureCodeEngineProcessAttemptExhausted,
		"engine.process.snapshot.failed":                     failureCodeEngineProcessSnapshotFailed,
		"engine.process.snapshot.unrestorable":               failureCodeEngineProcessSnapshotUnrestorable,
		"engine.process.start.failed":                        failureCodeEngineProcessStartFailed,
		"engine.termination.invalid":                         failureCodeEngineTerminationInvalid,
		"engine.tree.durability_conflict":                    failureCodeEngineTreeDurabilityConflict,
		"engine.tree.durability_failed":                      failureCodeEngineTreeDurabilityFailed,
		"engine.tree.incarnation_conflict":                   failureCodeEngineTreeIncarnationConflict,
		"execution.effect.invalid":                           failureCodeExecutionEffectInvalid,
		"execution.output.invalid":                           failureCodeExecutionOutputInvalid,
		"execution.snapshot.failed":                          failureCodeExecutionSnapshotFailed,
		"execution.snapshot.unrestorable":                    failureCodeExecutionSnapshotUnrestorable,
		"execution.step.failed":                              failureCodeExecutionStepFailed,
		"execution.transition.invalid":                       failureCodeExecutionTransitionInvalid,
	}
	seen := make(map[string]bool)
	for _, path := range frameworkProductionGoFiles(t) {
		if filepath.Dir(path) != "." {
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
			case "NewFailure", "newEngineFailure", "recordFailure":
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
