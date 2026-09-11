package interaction

import (
	"context"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestEngineRejectsInvalidInteractionCandidateBeforeModelCall(t *testing.T) {
	definition := advertisementTestDefinition(t)
	var calls atomic.Int32
	dispatcher, err := NewDispatcher(definition, DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls.Add(1)
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: corruptingCandidateDefinition{Definition: definition}, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("invalid-candidate")), ConfigurationDigest: agent.ComputeDigest([]byte("invalid-candidate-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := agent.EncodeInput(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, failed := result.Termination().Failure()
	if result.Status() != agent.StatusFailed || !failed || failure.Code() != "execution.snapshot.unrestorable" || calls.Load() != 0 {
		t.Fatalf("invalid candidate crossed admission: failure = %+v, calls = %d", failure, calls.Load())
	}
}

type corruptingCandidateDefinition struct{ *Definition }

func (c corruptingCandidateDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	restored, err := c.Definition.Restore(state)
	if err != nil {
		return nil, err
	}
	return corruptingCandidateExecution{execution: restored.(*execution)}, nil
}

type corruptingCandidateExecution struct{ *execution }

func (c corruptingCandidateExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	transition, err := c.execution.Step(ctx, signals)
	if err == nil {
		c.state.AdvertisedToolNames = []string{"unbound"}
	}
	return transition, err
}
