package chat_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

func assistantResult(text string) *chat.Output {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}
}

func TestFinishReason(t *testing.T) {
	for _, reason := range []chat.FinishReason{
		chat.FinishReasonStop,
		chat.FinishReasonLength,
		chat.FinishReasonToolCalls,
		chat.FinishReasonContentFilter,
		chat.FinishReasonRefusal,
		chat.FinishReasonOther,
	} {
		if !reason.Valid() {
			t.Errorf("%q must be valid", reason)
		}
		if reason.String() != string(reason) {
			t.Errorf("String(%q) = %q", reason, reason.String())
		}
	}
	if chat.FinishReason("").Valid() {
		t.Fatal("empty finish reason must be invalid")
	}
	if chat.FinishReason("provider-native").Valid() {
		t.Fatal("provider-native reason must map to Other plus output metadata")
	}
}

func TestMessageText(t *testing.T) {
	message := chat.NewAssistantMessage(
		chat.NewTextPart("hello "),
		chat.NewReasoningPart("hidden", nil),
		chat.NewTextPart("world"),
	)
	if got := message.Text(); got != "hello world" {
		t.Fatalf("Text = %q, want hello world", got)
	}
	var nilMessage *chat.Message
	if nilMessage.Text() != "" {
		t.Fatal("nil Message.Text must be empty")
	}
}

func TestResultValidate(t *testing.T) {
	valid := []*chat.Output{
		assistantResult("hello"),
		{FinishReason: chat.FinishReasonStop},
		{FinishReason: chat.FinishReasonStop, Metadata: &chat.OutputMetadata{}},
	}
	for i := range valid {
		if err := valid[i].Validate(); err != nil {
			t.Errorf("valid[%d]: %v", i, err)
		}
	}
}

func TestResultValidateRejectsInvalidValues(t *testing.T) {
	user := chat.NewUserMessage(chat.NewTextPart("hello"))
	invalidMessage := chat.Message{Role: chat.RoleAssistant}
	tests := []struct {
		name   string
		output *chat.Output
		also   error
	}{
		{name: "nil", output: nil},
		{name: "empty", output: &chat.Output{}},
		{name: "invalid message", output: &chat.Output{Message: &invalidMessage, FinishReason: chat.FinishReasonStop}, also: chat.ErrInvalidMessage},
		{name: "user message", output: &chat.Output{Message: &user, FinishReason: chat.FinishReasonStop}},
		{name: "unknown finish", output: &chat.Output{FinishReason: "future"}},
		{name: "invalid metadata", output: &chat.Output{FinishReason: chat.FinishReasonStop, Metadata: &chat.OutputMetadata{Extra: metadata.Map{"bad": json.RawMessage(`{`)}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.output.Validate()
			if !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("Validate error = %v, want ErrInvalidResponse", err)
			}
			if tt.also != nil && !errors.Is(err, tt.also) {
				t.Fatalf("Validate error = %v, also want %v", err, tt.also)
			}
		})
	}
}

func TestResultHelpersAndJSON(t *testing.T) {
	output := assistantResult("hello")
	output.Metadata = &chat.OutputMetadata{}
	if output.Text() != "hello" {
		t.Fatalf("Text = %q", output.Text())
	}
	if err := output.Metadata.Extra.Set("openai/logprobs", []float64{-0.1}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var got chat.Output
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, *output) {
		t.Fatalf("round trip = %#v, want %#v", got, *output)
	}

	var nilResult *chat.Output
	if nilResult.Text() != "" {
		t.Fatal("nil Output.Text must be empty")
	}
}

func TestNewOutputEstablishesCompleteOutput(t *testing.T) {
	message := chat.NewAssistantMessage(chat.NewTextPart("answer"))
	output, err := chat.NewOutput(&message, chat.FinishReasonStop, &chat.OutputMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if output.Text() != "answer" || output.FinishReason != chat.FinishReasonStop {
		t.Fatalf("output = %#v", output)
	}
	if _, err := chat.NewOutput(&message, "future", nil); !errors.Is(err, chat.ErrInvalidResponse) {
		t.Fatalf("invalid finish reason error = %v", err)
	}
}

func TestResultUnmarshalIsAtomic(t *testing.T) {
	output := assistantResult("keep")
	if err := json.Unmarshal([]byte(`{"finish_reason":"future"}`), output); !errors.Is(err, chat.ErrInvalidResponse) {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if output.Text() != "keep" {
		t.Fatalf("failed Unmarshal mutated output: %+v", output)
	}
	var nilResult *chat.Output
	if err := nilResult.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidResponse) {
		t.Fatalf("nil UnmarshalJSON error = %v", err)
	}
}

func TestResponseCloneOwnsNestedProtocolValues(t *testing.T) {
	reasoningTokens := int64(2)
	response, err := chat.NewResponse(assistantResult("original"), &chat.ResponseMetadata{
		Model: "model",
		Usage: &chat.Usage{OutputTokens: 2, ReasoningTokens: &reasoningTokens},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Metadata.Extra.Set("test/value", map[string]int{"count": 1}); err != nil {
		t.Fatal(err)
	}

	cloned := response.Clone()
	cloned.Metadata.Model = "mutated"
	cloned.Output.Message.Parts[0].Text = "mutated"
	*cloned.Metadata.Usage.ReasoningTokens = 9
	cloned.Metadata.Extra["test/value"][0] = 'x'

	if response.Metadata.Model != "model" ||
		response.Text() != "original" ||
		*response.Metadata.Usage.ReasoningTokens != 2 ||
		response.Metadata.Extra["test/value"][0] == 'x' {
		t.Fatalf("mutating clone changed source response: %#v", response)
	}
	var nilResponse *chat.Response
	if nilResponse.Clone() != nil {
		t.Fatal("nil Response.Clone returned a non-nil value")
	}
}

func TestResponseZeroAndNilHelpers(t *testing.T) {
	response := &chat.Response{}
	if response.Output != nil || response.Text() != "" {
		t.Fatal("empty response helpers must be nil/empty")
	}
	if _, err := json.Marshal(response); !errors.Is(err, chat.ErrInvalidResponse) {
		t.Fatalf("zero Response marshal error = %v, want ErrInvalidResponse", err)
	}
	var nilResponse *chat.Response
	if nilResponse.Text() != "" {
		t.Fatal("nil response text must be empty")
	}
}

func TestResponseValidateRejectsInvalidValues(t *testing.T) {
	invalidUsage := chat.Usage{InputTokens: -1}
	tests := []struct {
		name     string
		response *chat.Response
		also     error
	}{
		{name: "nil", response: nil},
		{name: "missing output", response: &chat.Response{}},
		{name: "ID whitespace", response: &chat.Response{Output: assistantResult("x"), Metadata: &chat.ResponseMetadata{ID: " id"}}},
		{name: "model whitespace", response: &chat.Response{Output: assistantResult("x"), Metadata: &chat.ResponseMetadata{Model: "model "}}},
		{name: "invalid output", response: &chat.Response{Output: &chat.Output{}}},
		{name: "invalid usage", response: &chat.Response{Output: assistantResult("x"), Metadata: &chat.ResponseMetadata{Usage: &invalidUsage}}, also: chat.ErrInvalidUsage},
		{name: "invalid metadata", response: &chat.Response{Output: assistantResult("x"), Metadata: &chat.ResponseMetadata{Extra: metadata.Map{"bad": json.RawMessage(`{`)}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.response.Validate()
			if !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("Validate error = %v, want ErrInvalidResponse", err)
			}
			if tt.also != nil && !errors.Is(err, tt.also) {
				t.Fatalf("Validate error = %v, also want %v", err, tt.also)
			}
		})
	}
}

func TestResponseJSONRoundTrip(t *testing.T) {
	reasoning := int64(4)
	cacheRead := int64(3)
	response, err := chat.NewResponse(assistantResult("hello"), &chat.ResponseMetadata{
		ID:    "response-1",
		Model: "model",
		Usage: &chat.Usage{InputTokens: 10, OutputTokens: 6, ReasoningTokens: &reasoning, CacheReadInputTokens: &cacheRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	if setErr := response.Metadata.Extra.Set("openai/system_fingerprint", "fp-1"); setErr != nil {
		t.Fatal(setErr)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var got chat.Response
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, *response) {
		t.Fatalf("round trip = %#v, want %#v", got, *response)
	}
}

func TestResponseUnmarshalIsAtomic(t *testing.T) {
	response := &chat.Response{Output: assistantResult("keep"), Metadata: &chat.ResponseMetadata{ID: "keep"}}
	if err := json.Unmarshal([]byte(`{"output":{}}`), response); !errors.Is(err, chat.ErrInvalidResponse) {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if response.Metadata.ID != "keep" {
		t.Fatalf("failed Unmarshal mutated response: %+v", response)
	}
	var nilResponse *chat.Response
	if err := nilResponse.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, chat.ErrInvalidResponse) {
		t.Fatalf("nil UnmarshalJSON error = %v", err)
	}
}

func TestResponseProtocolFieldsExcludeToolLoopState(t *testing.T) {
	typ := reflect.TypeFor[chat.Response]()
	want := []string{"Output", "Metadata"}
	if typ.NumField() != len(want) {
		t.Fatalf("Response has %d fields, want %d", typ.NumField(), len(want))
	}
	for i, name := range want {
		if typ.Field(i).Name != name {
			t.Errorf("Response field[%d] = %s, want %s", i, typ.Field(i).Name, name)
		}
	}
}

func TestResponseMetadataDistinguishesMissingAndZeroUsage(t *testing.T) {
	for _, test := range []struct {
		wire  string
		known bool
	}{
		{`{"id":"response"}`, false}, {`{"id":"response","usage":{}}`, true},
	} {
		var metadata chat.ResponseMetadata
		if err := json.Unmarshal([]byte(test.wire), &metadata); err != nil {
			t.Fatal(err)
		}
		if (metadata.Usage != nil) != test.known {
			t.Fatalf("usage presence in %s = %+v", test.wire, metadata.Usage)
		}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != test.wire {
			t.Fatalf("round trip = %s, want %s", encoded, test.wire)
		}
	}
}
