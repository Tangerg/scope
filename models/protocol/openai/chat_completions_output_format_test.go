package openai

import (
	"encoding/json"
	"errors"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

func TestOutputFormatMapsToChatAndResponsesNativeShapes(t *testing.T) {
	format, err := corechat.NewJSONSchemaOutputFormat(corechat.JSONSchemaConfig{
		Name: "answer", Description: "Answer payload",
		Schema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	chatFormat, err := mapChatOutputFormat(&format)
	if err != nil {
		t.Fatal(err)
	}
	if chatFormat.OfJSONSchema == nil || chatFormat.OfJSONSchema.JSONSchema.Name != "answer" {
		t.Fatalf("Chat response_format = %#v", chatFormat)
	}

	responsesFormat, err := mapResponsesOutputFormat(&format)
	if err != nil {
		t.Fatal(err)
	}
	if responsesFormat.OfJSONSchema == nil || responsesFormat.OfJSONSchema.Name != "answer" {
		t.Fatalf("Responses text.format = %#v", responsesFormat)
	}
}

func TestResponsesOutputFormatRejectsDuplicateExtensionSource(t *testing.T) {
	var extensions metadata.Extensions
	if err := extensions.Set(ResponsesRequestExtensionKey, map[string]any{
		"text": map[string]any{
			"verbosity": "low",
			"format":    map[string]any{"type": "json_object"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := rejectCoreOwnedResponsesExtension(extensions, ResponsesRequestExtensionKey); err == nil {
		t.Fatal("text.format extension unexpectedly accepted")
	}
}

func TestOutputFormatRejectsADialectWithoutNativeShape(t *testing.T) {
	format, err := corechat.NewJSONSchemaOutputFormat(corechat.JSONSchemaConfig{
		Name: "answer", Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	dialect := Dialect{NativeOutputFormat: func(formatType corechat.OutputFormatType) bool {
		return formatType != corechat.OutputFormatJSONSchema
	}}
	if err := applyChatOutputFormat(&format, nil, dialect); !errors.Is(err, corechat.ErrUnsupportedOutputFormat) {
		t.Fatalf("applyChatOutputFormat() error = %v, want ErrUnsupportedOutputFormat", err)
	}
}
