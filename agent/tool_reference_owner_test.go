package agent_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type toolSetResolver map[run.ProgramRef]run.Program[run.Payload, run.Payload]

func (t toolSetResolver) Resolve(reference run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	deployment, present := t[reference]
	if !present {
		return run.Program[run.Payload, run.Payload]{}, fmt.Errorf("deployment %s is unavailable", reference.Name())
	}
	return deployment, nil
}

type observedReference struct {
	call      string
	reference agent.ToolCallRef
	present   bool
}

// A parent that is not an Interaction may start a ToolSet child, but only the
// ChildKey an Interaction gives a Tool child carries a ToolCallRef, so two
// such children can never claim the same logical call.
func TestToolCallRefBelongsToTheToolChildKey(t *testing.T) {
	references := make(chan observedReference, 3)
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "probe", Description: "Report the call reference."},
		func(ctx context.Context, _ struct{}) (string, error) {
			invocation, found := agent.ToolInvocationFromContext(ctx)
			if !found {
				return "", errors.New("Tool invocation is missing")
			}
			reference, present := invocation.Reference()
			references <- observedReference{invocation.ToolCall().ID, reference, present}
			return "ok", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := agent.NewToolSet(agent.ToolSetConfig{
		Name: "test.reference.tools", Description: "Probe references.", Tools: []tool.Tool{executable},
		ImplementationDigest: run.ComputeDigest([]byte("tool")), ConfigurationDigest: run.ComputeDigest([]byte("config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	competition, err := coordination.NewFirstSuccess(coordination.FirstSuccessConfig{
		Name: "test.reference.competition", Description: "Run every candidate.", MaxCandidates: 3,
		Accept: func(context.Context, run.ChildKey, run.ChildOutcome) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition:           competition,
		ImplementationDigest: run.ComputeDigest([]byte("competition")), ConfigurationDigest: run.ComputeDigest([]byte("config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	keyed, err := agent.ToolChildKey(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	spec := func(key string, childKey run.ChildKey) run.ChildSpec {
		t.Helper()
		if childKey == (run.ChildKey{}) {
			if childKey, err = run.ParseChildKey(key); err != nil {
				t.Fatal(err)
			}
		}
		input, encodeErr := run.EncodePayload(map[string]chat.ToolCall{"call": {ID: key, Name: "probe", Arguments: `{}`}})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return run.ChildSpec{
			Key: childKey, ProgramRef: tools.Program().ProgramRef(), Input: input,
			Budget: run.Budget{Steps: run.NewQuota(4), Effects: run.NewQuota(4), Signals: run.NewQuota(4)},
		}
	}
	candidates := []run.ChildSpec{spec("first", run.ChildKey{}), spec("second", run.ChildKey{}), spec("keyed", keyed)}
	engine, err := run.New(t.Context(), run.Config{
		Store:           run.NewMemoryStore(),
		ProgramResolver: toolSetResolver{tools.Program().ProgramRef(): tools.Program()},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload(candidates)
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(t.Context(), parent, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	close(references)
	seen := 0
	for observed := range references {
		seen++
		want := observed.call == "keyed"
		if observed.present != want {
			t.Fatalf("call %s reference present = %t, want %t", observed.call, observed.present, want)
		}
		if want && (observed.reference.ProcessID() != root.Relation().ProcessID() || observed.reference.ModelCallSequence() != 1 || observed.reference.ToolCallIndex() != 0) {
			t.Fatalf("keyed reference = %v", observed.reference)
		}
	}
	if seen != 3 {
		t.Fatalf("observed %d Tool calls, want 3", seen)
	}
}
