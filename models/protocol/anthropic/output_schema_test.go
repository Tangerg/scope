package anthropic_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/models/protocol/anthropic"
)

func TestUnsupportedNativeSchemaFailsBeforeEveryEndpoint(t *testing.T) {
	derived, err := jsonschema.For[struct {
		Score int `json:"score"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"", string(derived.JSON()),
		`{"type":"object"}`,
		`{"type":"object","properties":{"name":{"type":"string","minLength":1}},"additionalProperties":false}`,
		`{"type":"object","properties":{"values":{"type":"array","items":{"type":"string"},"minItems":2}},"additionalProperties":false}`,
		`{"type":"object","properties":{"next":{"$ref":"#"}},"additionalProperties":false}`,
		`{"type":"object","properties":{"value":{"enum":[{"nested":true}]}} ,"additionalProperties":false}`,
		`{"type":"object","properties":{"value":{"type":"string","pattern":"\\bword\\b"}},"additionalProperties":false}`,
	} {
		t.Run(raw, func(t *testing.T) {
			boundary := new(numericTransport)
			model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
				APIKey: "test", BaseURL: "https://provider.example.test", HTTPClient: &http.Client{Transport: boundary},
				DefaultOptions: chat.Options{Model: "test"},
			}, testMessagesDialect())
			if err != nil {
				t.Fatal(err)
			}
			format, err := chat.NewOutputFormat(chat.OutputFormatJSON)
			if raw != "" {
				format, err = chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "result", Schema: json.RawMessage(raw)})
			}
			if err != nil {
				t.Fatal(err)
			}
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("answer")))
			if err != nil {
				t.Fatal(err)
			}
			request.Options.OutputFormat = &format
			_, callErr := model.Call(t.Context(), request)
			if !errors.Is(callErr, chat.ErrUnsupportedOutputFormat) || invocation.ClassifyFailure(callErr) != invocation.FailureNotExecuted {
				t.Fatalf("Call = %v", callErr)
			}
			var streamErr error
			for delta, err := range model.Stream(t.Context(), request) {
				if delta != nil {
					t.Fatalf("Stream emitted before admission: %#v", delta)
				}
				streamErr = err
			}
			if !errors.Is(streamErr, chat.ErrUnsupportedOutputFormat) || invocation.ClassifyFailure(streamErr) != invocation.FailureNotExecuted {
				t.Fatalf("Stream = %v", streamErr)
			}
			_, countErr := model.CountInputTokens(t.Context(), request)
			if !errors.Is(countErr, chat.ErrUnsupportedOutputFormat) || boundary.calls != 0 {
				t.Fatalf("CountInputTokens = %v, calls = %d", countErr, boundary.calls)
			}
		})
	}
}

func TestNativeSchemaResolvesURIFragmentPointersAtEveryEndpoint(t *testing.T) {
	for _, sample := range []struct{ name, key, reference string }{
		{"space", "a b", "#/$defs/a%20b"},
		{"percent", "a%b", "#/$defs/a%25b"},
		{"unicode", "中文", "#/$defs/%E4%B8%AD%E6%96%87"},
		{"literal plus", "a+b", "#/$defs/a+b"},
		{"encoded plus", "a+b", "#/$defs/a%2Bb"},
		{"slash", "a/b", "#/$defs/a~1b"},
		{"tilde", "a~b", "#/$defs/a~0b"},
		{"single unescape", "a~1b", "#/$defs/a~01b"},
		{"encoded delimiters", "a b", "#%2F$defs%2Fa%20b"},
		{"single percent decoding", "a%20b", "#/$defs/a%2520b"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			boundary := new(numericTransport)
			model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test", BaseURL: "https://provider.example.test", HTTPClient: &http.Client{Transport: boundary}, DefaultOptions: chat.Options{Model: "test"}}, testMessagesDialect())
			if err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf(`{"type":"object","properties":{"value":{"$ref":%q}},"additionalProperties":false,"$defs":{%q:{"type":"string"}}}`, sample.reference, sample.key)
			format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "result", Schema: json.RawMessage(raw)})
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("answer"))}, Options: chat.Options{OutputFormat: &format}}
			_, callErr := model.Call(t.Context(), request)
			for _, err := range model.Stream(t.Context(), request) {
				if errors.Is(err, chat.ErrUnsupportedOutputFormat) {
					t.Fatal(err)
				}
			}
			_, countErr := model.CountInputTokens(t.Context(), request)
			if errors.Is(callErr, chat.ErrUnsupportedOutputFormat) || errors.Is(countErr, chat.ErrUnsupportedOutputFormat) || boundary.calls != 3 {
				t.Fatalf("calls=%d call=%v count=%v", boundary.calls, callErr, countErr)
			}
		})
	}
}

func TestNativeSchemaRetainsOptionalFieldsAndExactConstants(t *testing.T) {
	boundary := new(numericTransport)
	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
		APIKey: "test", BaseURL: "https://provider.example.test", HTTPClient: &http.Client{Transport: boundary},
		DefaultOptions: chat.Options{Model: "test"},
	}, testMessagesDialect())
	if err != nil {
		t.Fatal(err)
	}
	const raw = `{"type":"object","properties":{"optional":{"$ref":"#/$defs/value"}},"additionalProperties":false,"$defs":{"value":{"type":"integer","const":9007199254740993}}}`
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "result", Schema: json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("answer")))
	if err != nil {
		t.Fatal(err)
	}
	request.Options.OutputFormat = &format
	_, _ = model.Call(t.Context(), request)
	if boundary.calls != 1 {
		t.Fatalf("valid optional schema was not sent, calls=%d", boundary.calls)
	}
	var wire struct {
		OutputConfig struct {
			Format struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"format"`
		} `json:"output_config"`
	}
	if decodeErr := jsonv2.Unmarshal(boundary.body, &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	projected, err := jsonschema.Parse(wire.OutputConfig.Format.Schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, instance := range []string{`{}`, `{"optional":9007199254740993}`} {
		if err := projected.Validate([]byte(instance)); err != nil {
			t.Fatal(err)
		}
	}
	if err := projected.Validate([]byte(`{"optional":9007199254740992}`)); err == nil {
		t.Fatal("exact constant was weakened")
	}
}
