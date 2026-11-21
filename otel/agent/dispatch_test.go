package agent_test

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// A built-in strategy binds its own Dispatcher inside Compile, so only a
// Runtime-wide dispatch middleware can instrument its Effects.
func TestDispatchMiddlewareSpansABuiltInStrategysEffects(t *testing.T) {
	h := newObserverHarness(t)
	runtime, err := run.New(t.Context(), run.Config{
		Build: "otel-test", Store: h.middleware.Store(run.NewMemoryStore()),
		DispatchMiddlewares: []run.DispatchMiddleware{h.middleware.Dispatch},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		message := chat.NewAssistantMessage(chat.NewTextPart("done"))
		return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
	})
	program, err := interaction.Compile(runtime.Compiler(), interaction.Config{
		Name: "test.instrumented", Description: "Answer once.", Model: interaction.CallModel(model),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(t.Context(), program, interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}}); err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	for _, span := range h.spans.Ended() {
		if span.Name() == "agent.dispatch" {
			dispatches++
		}
	}
	if dispatches != 1 {
		t.Fatalf("dispatch spans = %d, want the model call's", dispatches)
	}
}
