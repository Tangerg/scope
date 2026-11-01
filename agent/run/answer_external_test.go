package run_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestRootAnswersExactChildWaitAndReconcilesAfterTerminal(t *testing.T) {
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	def, err := coordination.NewInputGate(coordination.InputGateConfig{Name: "test.approval", Description: "Wait for an explicit answer.", RequestSchema: schema, AnswerSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := run.Bind[string, run.Signal](run.BindingConfig{Definition: def, ImplementationDigest: run.ComputeDigest([]byte("gate")), ConfigurationDigest: run.ComputeDigest([]byte("gate config"))})
	if err != nil {
		t.Fatal(err)
	}
	call, err := workflow.Call(workflow.CallConfig{ID: "approval", Deployment: gate.Erase()})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := workflow.Compile[string, run.Signal](t.Context(), workflow.Config{Name: "test.review", Description: "Delegate approval to a child.", Stages: []workflow.Stage{call}, BuildIdentity: run.ComputeDigest([]byte("review"))})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	h, err := rt.Start(t.Context(), outer, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: "review"}, Input: "grant permission"})
	if err != nil {
		t.Fatal(err)
	}
	var wait run.WaitID
	for observed, err := range h.Observe(t.Context(), run.Cursor{}, 128) {
		if err != nil {
			t.Fatal(err)
		}
		if !observed.Snapshot.Valid() {
			continue
		}
		for _, p := range observed.Snapshot.ProcessSnapshots() {
			if kind, ok := p.WaitKind(); ok && kind == run.WaitKindExternal {
				if p.Relation().ProcessID() == h.ID() {
					t.Fatal("expected a child wait")
				}
				wait, _ = p.WaitID()
				break
			}
		}
		if wait.Valid() {
			break
		}
	}
	if !wait.Valid() {
		t.Fatal("no external wait")
	}
	if err = h.Answer(t.Context(), wait, "approved"); err != nil {
		t.Fatal(err)
	}
	if err = h.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := h.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := result.Output()
	if err != nil || string(reply.Payload()) != `"approved"` {
		t.Fatalf("child reply=%s err=%v", reply.Payload(), err)
	}
	if err = h.Answer(t.Context(), wait, "approved"); err != nil {
		t.Fatalf("duplicate after completion: %v", err)
	}
	if err = h.Answer(t.Context(), wait, "denied"); !errors.Is(err, run.ErrSignalConflict) {
		t.Fatalf("changed answer: %v", err)
	}
	if err = rt.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}
