package interaction

import (
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// A model response can pass its settlement envelope bound yet, once wrapped in
// the final Output, exceed the deliverable payload size. Completion reuses the
// Output's canonical encoding as its admission, so an undeliverable response
// becomes a definite failure instead of an opaque encode error that leaves the
// Interaction unable to finish.
func TestCompleteFailsDefinitelyWhenFinalOutputExceedsDeliverableSize(t *testing.T) {
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.oversize", Description: "Check the deliverable output bound.",
	})
	if err != nil {
		t.Fatal(err)
	}
	execution := &execution{definition: definition, state: executionState{
		WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("go"))}},
		ModelCallCount: 1,
	}}

	// Structurally valid, but the final Output wrapper pushes it past the bound.
	message := chat.NewAssistantMessage(chat.NewTextPart(strings.Repeat("x", agent.MaxPayloadBytes)))
	output := Output{
		ModelResponse: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}},
		ModelCalls:    1,
	}
	if validateErr := output.Validate(); validateErr != nil {
		t.Fatalf("test output is not structurally valid: %v", validateErr)
	}

	transition, completeErr := execution.complete(0, output)
	if completeErr != nil {
		t.Fatalf("undeliverable output returned an opaque error, not a definite failure: %v", completeErr)
	}
	failure, failed := transition.Failure()
	if !failed || failure.Kind() != agent.FailureKindExecution || failure.Code() != "interaction.limit.response_bytes" {
		t.Fatalf("transition = %+v, want a definite interaction.limit.response_bytes failure", transition)
	}
	if execution.state.Completed {
		t.Fatal("an undeliverable response must not mark the Interaction complete")
	}
}
