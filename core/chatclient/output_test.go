package chatclient

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestClientOutputUsesTypedOutput(t *testing.T) {
	want := recipe{Name: "tea", Steps: []string{"boil", "steep"}}
	var callFormat *chat.OutputFormat
	model := callOnly{
		call: func(_ context.Context, request *chat.Request) (*chat.Response, error) {
			callFormat = request.Options.OutputFormat.Clone()
			return responseWithText(t, `{"name":"tea","steps":["boil","steep"]}`), nil
		},
	}
	client, err := New(model, Config{})
	if err != nil {
		t.Fatal(err)
	}
	request := textRequest("make tea")
	called, err := client.Output(t.Context(), request, JSON[recipe]())
	if err != nil || !reflect.DeepEqual(called, want) {
		t.Fatalf("Call = (%#v, %v), want %#v", called, err, want)
	}
	if callFormat == nil || callFormat.Type != chat.OutputFormatJSON {
		t.Errorf("call output format = %#v, want JSON", callFormat)
	}
	if request.Options.OutputFormat != nil {
		t.Fatal("typed output mutated caller-owned request")
	}
}

func TestClientOutputPreservesUnsuccessfulCompletion(t *testing.T) {
	for _, reason := range []chat.FinishReason{
		chat.FinishReasonLength, chat.FinishReasonToolCalls, chat.FinishReasonContentFilter,
		chat.FinishReasonRefusal, chat.FinishReasonOther,
	} {
		t.Run(reason.String(), func(t *testing.T) {
			response := textResponse(`{"name":"tea","steps":[]}`)
			response.Output.FinishReason = reason
			client, err := New(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return response, nil
			}), Config{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Output(t.Context(), textRequest("tea"), JSON[recipe]())
			completion, ok := errors.AsType[*OutputCompletionError](err)
			if !errors.Is(err, ErrInvalidOutput) || !ok || completion.FinishReason != reason {
				t.Fatalf("error=%v, want completion reason %s", err, reason)
			}
		})
	}
}

func TestClientOutputPreservesRefusalDiagnostic(t *testing.T) {
	response := &chat.Response{Output: &chat.Output{
		FinishReason: chat.FinishReasonRefusal,
		Message:      new(chat.NewAssistantMessage(chat.NewRefusalPart("declined"))),
	}}
	client, err := New(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return response, nil
	}), Config{})
	if err != nil {
		t.Fatal(err)
	}
	value, err := client.Output(t.Context(), textRequest("request"), Text())
	completion, ok := errors.AsType[*OutputCompletionError](err)
	if value != "" || !errors.Is(err, ErrInvalidOutput) || !ok ||
		completion.FinishReason != chat.FinishReasonRefusal || completion.Refusal != "declined" {
		t.Fatalf("value=%q error=%v, want identifiable refusal", value, err)
	}
}

func TestClientOutputRequiresCompletedTextParts(t *testing.T) {
	for _, test := range []struct {
		name    string
		message *chat.Message
		want    string
		refusal string
		invalid bool
	}{
		{name: "reasoning and text", message: new(chat.NewAssistantMessage(chat.NewReasoningPart("internal reasoning", nil), chat.NewTextPart("first"), chat.NewTextPart("second"))), want: "firstsecond"},
		{name: "refusal despite stop", message: new(chat.NewAssistantMessage(chat.NewTextPart("partial"), chat.NewRefusalPart("declined"))), refusal: "declined", invalid: true},
		{name: "tool request despite stop", message: new(chat.NewAssistantMessage(chat.NewTextPart("partial"), chat.NewToolCallPart(chat.ToolCall{ID: "pending", Name: "lookup", Arguments: `{}`}))), invalid: true},
		{name: "reasoning alone", message: new(chat.NewAssistantMessage(chat.NewReasoningPart("still reasoning", nil))), invalid: true},
		{name: "absent message", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop, Message: test.message}}
			client, err := New(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return response, nil }), Config{})
			if err != nil {
				t.Fatal(err)
			}
			value, err := client.Output(t.Context(), textRequest("request"), Text())
			if value != test.want || errors.Is(err, ErrInvalidOutput) != test.invalid {
				t.Fatalf("value=%q error=%v", value, err)
			}
			if test.refusal != "" {
				completion, ok := errors.AsType[*OutputCompletionError](err)
				if !ok || completion.Refusal != test.refusal {
					t.Fatalf("refusal error=%v", err)
				}
			}
		})
	}
}

func TestClientOutputSnapshotsSchema(t *testing.T) {
	format, err := JSONSchema[recipe](JSONSchemaConfig{Name: "recipe"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(callOnly{call: func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		got := request.Options.OutputFormat
		if got == nil || got.Type != chat.OutputFormatJSONSchema || got.Name != "recipe" || got.Schema[0] != '{' {
			t.Fatalf("output format = %#v", got)
		}
		got.Schema[0] = '['
		return responseWithText(t, `{"name":"tea","steps":[]}`), nil
	}}, Config{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Output(t.Context(), textRequest("tea"), format); err != nil {
		t.Fatal(err)
	}
	if format.contract.Schema[0] != '{' {
		t.Fatal("provider mutation escaped the output format snapshot")
	}
}

func TestClientOutputRejectsDuplicateOrInvalidFormat(t *testing.T) {
	var calls int
	client, err := New(callOnly{call: func(context.Context, *chat.Request) (*chat.Response, error) {
		calls++
		return responseWithText(t, `{}`), nil
	}}, Config{})
	if err != nil {
		t.Fatal(err)
	}

	request := textRequest("hello")
	requestFormat, err := chat.NewOutputFormat(chat.OutputFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	request.Options.OutputFormat = &requestFormat
	if _, err := client.Output(t.Context(), request, JSON[recipe]()); !errors.Is(err, ErrInvalidOutputFormat) {
		t.Fatalf("duplicate output format = %v, want ErrInvalidOutputFormat", err)
	}
	if calls != 0 {
		t.Fatalf("model called %d times for duplicate output format", calls)
	}

	if _, err := client.Output(t.Context(), textRequest("hello"), OutputFormat[recipe]{}); !errors.Is(err, ErrInvalidOutputFormat) {
		t.Fatalf("zero output format = %v, want ErrInvalidOutputFormat", err)
	}
	var zeroClient Client
	if _, err := zeroClient.Output(t.Context(), textRequest("hello"), JSON[recipe]()); !errors.Is(err, ErrNilClient) {
		t.Fatalf("nil client = %v, want ErrNilClient", err)
	}
}

func TestClientOutputPreservesTransportErrors(t *testing.T) {
	upstream := errors.New("upstream")
	client, err := New(callOnly{call: func(context.Context, *chat.Request) (*chat.Response, error) {
		return nil, upstream
	}}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Output(t.Context(), textRequest("hello"), JSON[recipe]()); !errors.Is(err, upstream) {
		t.Fatalf("Call error = %v, want upstream", err)
	}
}
