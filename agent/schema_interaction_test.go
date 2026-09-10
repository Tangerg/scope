package agent_test

import (
	"encoding/json"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

func TestSchemaForAcceptsInteractionProviderMetadataAndReasoningSignature(t *testing.T) {
	reasoning := chat.NewReasoningPart("provider reasoning", []byte{0, 1, 2, 255})
	reasoning.Metadata = metadata.Map{
		"openai/reasoning_field":    json.RawMessage(`"reasoning_content"`),
		"openai/reasoning_provider": json.RawMessage(`"deepseek"`),
	}
	message := chat.NewAssistantMessage(reasoning, chat.NewTextPart("provider answer"))
	response, err := chat.NewResponse(&chat.Output{
		Message:      &message,
		FinishReason: chat.FinishReasonStop,
	}, &chat.ResponseMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	response.Metadata.Extra = metadata.Map{
		"deepseek/openai_stream_chunk": json.RawMessage(`{"id":"chunk-1","choices":[]}`),
	}
	value := interaction.Output{
		Source:        interaction.CompletionSourceModelResponse,
		ModelResponse: response,
		ModelCalls:    1,
	}
	if validateErr := value.Validate(); validateErr != nil {
		t.Fatal(validateErr)
	}

	schema, err := agent.SchemaFor[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	output, err := agent.EncodeOutput(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.ValidateOutput(output); err != nil {
		t.Fatalf("ValidateOutput(provider response) error = %v; schema = %s", err, schema.JSON())
	}
}
