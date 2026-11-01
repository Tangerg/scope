package agent

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/run"
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
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: corruptingCandidateDefinition{Definition: definition}, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("invalid-candidate")), ConfigurationDigest: run.ComputeDigest([]byte("invalid-candidate-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := run.EncodePayload(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, failed := result.Termination().Failure()
	if result.Termination().Status() != run.StatusFailed || !failed || failure.Code() != "execution.snapshot.unrestorable" || calls.Load() != 0 {
		t.Fatalf("invalid candidate crossed admission: failure = %+v, calls = %d", failure, calls.Load())
	}
}

type corruptingCandidateDefinition struct{ *Definition }

func (c corruptingCandidateDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	restored, err := c.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return corruptingCandidateExecution{execution: restored.(*execution)}, nil
}

type corruptingCandidateExecution struct{ *execution }

func (c corruptingCandidateExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	transition, err := c.execution.Step(ctx, signals)
	if err == nil {
		c.state.AdvertisedToolNames = []string{"unbound"}
	}
	return transition, err
}
