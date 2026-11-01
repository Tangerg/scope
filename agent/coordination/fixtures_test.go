package coordination_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func bind(t testing.TB, definition run.Definition, dispatcher run.Dispatcher) run.Program[run.Payload, run.Payload] {
	t.Helper()
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("coordination-test-artifact")),
		ConfigurationDigest:  run.ComputeDigest([]byte(t.Name() + "/" + definition.Descriptor().Name())),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func deadlineBinding(t testing.TB, dispatcher run.Dispatcher) run.Program[run.Payload, run.Payload] {
	t.Helper()
	definition, err := coordination.NewDeadline(coordination.DeadlineConfig{
		Name: "test.deadline", Description: "Wait for the declared absolute instant.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return bind(t, definition, dispatcher)
}

func inputGate(t testing.TB) *coordination.InputGate {
	t.Helper()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := coordination.NewInputGate(coordination.InputGateConfig{
		Name: "test.input_gate", Description: "Wait for one identified answer.",
		RequestSchema: schema, AnswerSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func competition(t testing.TB, accept coordination.SuccessPredicate, maximum uint32) *coordination.FirstSuccess {
	t.Helper()
	definition, err := coordination.NewFirstSuccess(coordination.FirstSuccessConfig{
		Name: "test.first_success", Description: "Select the first acceptable observed result.",
		MaxCandidates: maximum, Accept: accept,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func candidate(t testing.TB, key string, deployment run.Program[run.Payload, run.Payload], input run.Payload) run.ChildSpec {
	t.Helper()
	childKey, err := run.ParseChildKey(key)
	if err != nil {
		t.Fatal(err)
	}
	budget := run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(8), Signals: run.NewQuota(16)}
	return run.ChildSpec{Key: childKey, ProgramRef: deployment.ProgramRef(), Input: input, Budget: budget}
}

func encodedInput[T any](t testing.TB, value T) run.Payload {
	t.Helper()
	input, err := run.EncodePayload(value)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func inspect(t testing.TB, engine *run.Runtime, process *run.Handle[run.Payload]) run.ProcessInspection {
	t.Helper()
	tree, err := engine.InspectTree(context.Background(), process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	fact, present := tree.Process(process.Relation().ProcessID())
	if !present {
		t.Fatal("process is missing from its tree")
	}
	return fact
}

func result(t testing.TB, process *run.Handle[run.Payload]) run.Result[run.Payload] {
	t.Helper()
	value, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func completedOutput[T any](t testing.TB, process *run.Handle[run.Payload]) T {
	t.Helper()
	value := result(t, process)
	output, present := value.Termination().Output()
	if value.Termination().Status() != run.StatusCompleted || !present {
		t.Fatalf("process ended with %s: %+v", value.Termination().Status(), value.Termination())
	}
	decoded, err := output.Decode[T]()
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func closeEngine(t testing.TB, engine *run.Runtime) {
	t.Helper()
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type resolver map[run.ProgramRef]run.Program[run.Payload, run.Payload]

func (r resolver) Resolve(reference run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	deployment, present := r[reference]
	if !present {
		return run.Program[run.Payload, run.Payload]{}, fmt.Errorf("test deployment %s is unavailable", reference.Name())
	}
	return deployment, nil
}

// externalSignal rebuilds the Signal an external sender delivers. The Runtime
// mints Signals at runtime, so tests reconstruct one through its wire form.
func externalSignal(t testing.TB, waitID string, payload any) run.Signal {
	t.Helper()
	wire, err := jsonv2.Marshal(struct {
		ID      string `json:"id"`
		WaitID  string `json:"wait_id,omitempty"`
		Payload any    `json:"payload"`
	}{ID: "signal:external", WaitID: waitID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var signal run.Signal
	if err := jsonv2.Unmarshal(wire, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}
