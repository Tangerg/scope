package interaction

import (
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// A response fitting its settlement envelope can still exceed the deliverable
// payload size once wrapped in the final Output; completion must then fail
// definitely, not with an opaque encode error that strands the Interaction.
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
