package runtest

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

type panickingCallbacks struct{ cause error }

func (p panickingCallbacks) Descriptor() run.Descriptor { panic(p.cause) }
func (p panickingCallbacks) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	panic(p.cause)
}
func (p panickingCallbacks) Start(run.Payload) (run.Execution, error) { panic(p.cause) }
func (p panickingCallbacks) Restore(context.Context, run.ExecutionState) (run.Execution, error) {
	panic(p.cause)
}
func (p panickingCallbacks) Step(context.Context, run.Turn) (run.Transition, error) {
	panic(p.cause)
}
func (p panickingCallbacks) Snapshot() (run.ExecutionState, error) { panic(p.cause) }

func TestDefinitionConformancePreservesTypedPanicEvidence(t *testing.T) {
	cause := errors.New("callback panic cause")
	fixture := panickingCallbacks{cause: cause}
	for operation, call := range map[string]func() error{
		"Definition.Descriptor": func() error { _, err := callDescriptor(fixture); return err },
		"Definition.Start":      func() error { _, err := callStart(fixture, run.Payload{}); return err },
		"Definition.Restore":    func() error { _, err := callRestore(t.Context(), fixture, run.ExecutionState{}); return err },
		"Execution.Step":        func() error { _, err := callStep(t.Context(), fixture, definitionTurn(1)); return err },
		"Execution.Snapshot":    func() error { _, err := callSnapshot(fixture); return err },
	} {
		t.Run(operation, func(t *testing.T) {
			err := call()
			panicErr, ok := errors.AsType[*run.CallbackPanicError](err)
			if !ok || panicErr.Operation != operation || !errors.Is(err, cause) {
				t.Fatalf("panic evidence=%v", err)
			}
		})
	}
}
