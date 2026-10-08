package interaction_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/coordination"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type toolSetResolver map[agent.DeploymentRef]agent.Deployment

func (t toolSetResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, present := t[reference]
	if !present {
		return agent.Deployment{}, fmt.Errorf("deployment %s is unavailable", reference.Name())
	}
	return deployment, nil
}

type observedReference struct {
	call      string
	reference interaction.ToolCallRef
	present   bool
}

// A parent that is not an Interaction may start a ToolSet child, but only the
// ChildKey an Interaction gives a Tool child carries a ToolCallRef, so two
// such children can never claim the same logical call.
func TestToolCallRefBelongsToTheToolChildKey(t *testing.T) {
	references := make(chan observedReference, 3)
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "probe", Description: "Report the call reference."},
		func(ctx context.Context, _ struct{}) (string, error) {
			invocation, found := interaction.ToolInvocationFromContext(ctx)
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
	tools, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "test.reference.tools", Description: "Probe references.", Tools: []tool.Tool{executable},
		ImplementationDigest: agent.ComputeDigest([]byte("tool")), ConfigurationDigest: agent.ComputeDigest([]byte("config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	competition, err := coordination.NewFirstSuccess(coordination.FirstSuccessConfig{
		Name: "test.reference.competition", Description: "Run every candidate.", MaxCandidates: 3,
		Accept: func(context.Context, agent.ChildKey, agent.ChildOutcome) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           competition,
		ImplementationDigest: agent.ComputeDigest([]byte("competition")), ConfigurationDigest: agent.ComputeDigest([]byte("config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	keyed, err := interaction.ToolChildKey(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	spec := func(key string, childKey agent.ChildKey) agent.ChildSpec {
		t.Helper()
		if childKey == (agent.ChildKey{}) {
			if childKey, err = agent.ParseChildKey(key); err != nil {
				t.Fatal(err)
			}
		}
		input, encodeErr := agent.EncodePayload(map[string]chat.ToolCall{"call": {ID: key, Name: "probe", Arguments: `{}`}})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return agent.ChildSpec{
			Key: childKey, DeploymentRef: tools.Deployment().DeploymentRef(), Input: input,
			Budget: agent.Budget{Steps: agent.NewQuota(4), Effects: agent.NewQuota(4), Signals: agent.NewQuota(4)},
		}
	}
	candidates := []agent.ChildSpec{spec("first", agent.ChildKey{}), spec("second", agent.ChildKey{}), spec("keyed", keyed)}
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeCommitter:      agent.NewMemoryTreeCommitter(),
		DeploymentResolver: toolSetResolver{tools.Deployment().DeploymentRef(): tools.Deployment()},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodePayload(candidates)
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(t.Context(), parent, input)
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
		if want && (observed.reference.ProcessID() != root.ID() || observed.reference.ModelCallSequence() != 1 || observed.reference.ToolCallIndex() != 0) {
			t.Fatalf("keyed reference = %v", observed.reference)
		}
	}
	if seen != 3 {
		t.Fatalf("observed %d Tool calls, want 3", seen)
	}
}
