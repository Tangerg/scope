package agenttest

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
)

type panickingCallbacks struct{ cause error }

func (p panickingCallbacks) Descriptor() agent.Descriptor                 { panic(p.cause) }
func (p panickingCallbacks) Start(agent.Payload) (agent.Execution, error) { panic(p.cause) }
func (p panickingCallbacks) Restore(context.Context, agent.ExecutionState) (agent.Execution, error) {
	panic(p.cause)
}
func (p panickingCallbacks) Step(context.Context, []agent.Signal) (agent.Transition, error) {
	panic(p.cause)
}
func (p panickingCallbacks) Snapshot() (agent.ExecutionState, error) { panic(p.cause) }

func TestDefinitionConformancePreservesTypedPanicEvidence(t *testing.T) {
	cause := errors.New("callback panic cause")
	fixture := panickingCallbacks{cause: cause}
	for operation, call := range map[string]func() error{
		"Definition.Descriptor": func() error { _, err := callDescriptor(fixture); return err },
		"Definition.Start":      func() error { _, err := callStart(fixture, agent.Payload{}); return err },
		"Definition.Restore":    func() error { _, err := callRestore(t.Context(), fixture, agent.ExecutionState{}); return err },
		"Execution.Step":        func() error { _, err := callStep(t.Context(), fixture, nil); return err },
		"Execution.Snapshot":    func() error { _, err := callSnapshot(fixture); return err },
	} {
		t.Run(operation, func(t *testing.T) {
			err := call()
			panicErr, ok := errors.AsType[*agent.CallbackPanicError](err)
			if !ok || panicErr.Operation != operation || !errors.Is(err, cause) {
				t.Fatalf("panic evidence=%v", err)
			}
		})
	}
}
