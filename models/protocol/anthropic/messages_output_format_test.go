package anthropic

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestProtocolOutputFormatUsesNativeSchemaOrRejects(t *testing.T) {
	schema, err := corechat.NewJSONSchemaOutputFormat(corechat.JSONSchemaConfig{
		Name: "answer", Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	params := anthropicsdk.MessageNewParams{}
	if mapProtocolOutputFormatErr := mapProtocolOutputFormat(&schema, Dialect{NativeJSONSchema: true}, &params); mapProtocolOutputFormatErr != nil {
		t.Fatal(mapProtocolOutputFormatErr)
	}
	if len(params.OutputConfig.Format.Schema) == 0 || len(params.System) != 0 {
		t.Fatalf("native schema mapping = %#v", params)
	}

	jsonFormat, err := corechat.NewOutputFormat(corechat.OutputFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	params = anthropicsdk.MessageNewParams{}
	if err := mapProtocolOutputFormat(&jsonFormat, Dialect{NativeJSONSchema: true}, &params); err != nil {
		t.Fatal(err)
	}
	if params.OutputConfig.Format.Schema["type"] != "object" || len(params.System) != 0 {
		t.Fatalf("native JSON mapping = %#v", params)
	}

	params = anthropicsdk.MessageNewParams{}
	if err := mapProtocolOutputFormat(&schema, Dialect{}, &params); !errors.Is(err, corechat.ErrUnsupportedOutputFormat) {
		t.Fatalf("unsupported schema error = %v", err)
	}
}

func TestOutputConfigExtensionKeepsNonFormatFields(t *testing.T) {
	fields := map[string]any{
		"output_config": map[string]any{
			"future_field": "preserved",
		},
	}
	config, err := decodeOutputConfig(fields, RequestExtensionKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := jsonv2.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["future_field"] != "preserved" {
		t.Fatalf("output_config = %s", encoded)
	}
	if _, exists := fields["output_config"]; exists {
		t.Fatal("decoded output_config remained in top-level extra fields")
	}

	for _, owned := range []map[string]any{
		{"format": map[string]any{}},
		{"format": nil},
		{"effort": "high"},
	} {
		fields = map[string]any{"output_config": owned}
		if _, err := decodeOutputConfig(fields, RequestExtensionKey); err == nil {
			t.Fatalf("owned fields %#v unexpectedly accepted", owned)
		}
	}
}

func TestProtocolRequestMapsCoreReasoningEffort(t *testing.T) {
	request := validNativeRequest(t)
	request.Options.ReasoningEffort = "max"
	params, err := mapProtocolRequest(
		corechat.Options{Model: "claude-opus-4-6"},
		request,
		Dialect{Provider: "anthropic"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if params.OutputConfig.Effort != anthropicsdk.OutputConfigEffortMax {
		t.Fatalf("output_config.effort = %q", params.OutputConfig.Effort)
	}

	request.Options.ReasoningEffort = "turbo"
	if _, err := mapProtocolRequest(corechat.Options{Model: "claude-opus-4-6"}, request, Dialect{Provider: "anthropic"}); err == nil || !strings.Contains(err.Error(), "unsupported value") {
		t.Fatalf("unknown effort error = %v", err)
	}
}
