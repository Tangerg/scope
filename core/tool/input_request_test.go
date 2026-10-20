package tool_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

type confirmingTool struct{}

func (confirmingTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{Name: "confirm", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (confirmingTool) Call(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
	if continuation, resumed := invocation.Continuation(); resumed {
		return tool.Result{Output: chat.NewTextToolOutput(string(continuation.State()) + ":" + string(continuation.Response()))}, nil
	}
	schema, err := jsonschema.Parse([]byte(`{"const":9007199254740993}`))
	if err != nil {
		return tool.Result{}, err
	}
	request, err := tool.NewInputRequest(tool.InputRequestConfig{Prompt: []byte(`"confirm"`), ResponseSchema: schema, ContinuationState: []byte(`{"id":9007199254740993}`)})
	return tool.Result{InputRequest: &request}, err
}

func TestExplicitContinuationSurvivesWireAndIsFrozenAtPrepare(t *testing.T) {
	binding, err := tool.Bind(confirmingTool{})
	if err != nil {
		t.Fatal(err)
	}
	call := chat.ToolCall{ID: "confirm-1", Name: "confirm", Arguments: `{}`}
	initial, err := binding.Contract().Prepare(call, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := binding.Call(t.Context(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if validationErr := result.Validate(); validationErr != nil || result.InputRequest == nil {
		t.Fatalf("result=%+v error=%v", result, validationErr)
	}
	encoded, err := jsonv2.Marshal(result.InputRequest)
	if err != nil {
		t.Fatal(err)
	}
	var request tool.InputRequest
	if decodeErr := jsonv2.Unmarshal(encoded, &request); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if _, answerErr := request.Continuation([]byte(`9007199254740992`)); !errors.Is(answerErr, tool.ErrInvalidInputRequest) {
		t.Fatalf("rounded answer=%v", answerErr)
	}
	continuation, err := request.Continuation([]byte(`9007199254740993`))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := binding.Contract().Prepare(call, &continuation)
	if err != nil {
		t.Fatal(err)
	}
	continuation = tool.Continuation{}
	completed, err := binding.Call(t.Context(), resumed)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := completed.Output.Text()
	if text != `{"id":9007199254740993}:9007199254740993` || completed.InputRequest != nil {
		t.Fatalf("completed=%+v", completed)
	}
	again, err := binding.Contract().Prepare(call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, resumed := again.Continuation(); resumed {
		t.Fatal("new call inherited continuation")
	}
	if _, err := binding.Contract().Prepare(call, &continuation); !errors.Is(err, tool.ErrInvalidInvocation) {
		t.Fatalf("zero continuation=%v", err)
	}
	if err := (tool.Result{Output: chat.NewTextToolOutput("complete"), InputRequest: &request}).Validate(); !errors.Is(err, tool.ErrInvalidResult) {
		t.Fatalf("ambiguous result=%v", err)
	}
}

func TestInputRequestAdmissionRejectsInvalidWireWithoutReplacingValue(t *testing.T) {
	var zero tool.InputRequest
	if _, err := zero.Continuation([]byte(`true`)); !errors.Is(err, tool.ErrInvalidInputRequest) {
		t.Fatal(err)
	}
	if err := (*tool.InputRequest)(nil).UnmarshalJSON([]byte(`{}`)); !errors.Is(err, tool.ErrInvalidInputRequest) {
		t.Fatal(err)
	}
	schema, err := jsonschema.Parse([]byte(`true`))
	if err != nil {
		t.Fatal(err)
	}
	valid := tool.InputRequestConfig{Prompt: []byte(`{}`), ResponseSchema: schema, ContinuationState: []byte(`null`)}
	for _, invalid := range []tool.InputRequestConfig{{}, {Prompt: []byte(`{}`), ContinuationState: []byte(`null`)}, {Prompt: []byte(`{}`), ResponseSchema: schema}, {Prompt: []byte(`{`), ResponseSchema: schema, ContinuationState: []byte(`null`)}} {
		if _, configErr := tool.NewInputRequest(invalid); !errors.Is(configErr, tool.ErrInvalidInputRequest) {
			t.Fatalf("invalid config accepted: %v", configErr)
		}
	}
	request, err := tool.NewInputRequest(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, wire := range []string{`null`, `{}`, `{"prompt":{},"response_schema":{"type":"invalid"},"continuation_state":null}`, `{"prompt":{},"response_schema":true,"continuation_state":null,"extra":true}`} {
		if err := jsonv2.Unmarshal([]byte(wire), &request); err == nil {
			t.Fatalf("accepted %s", wire)
		}
		if !request.Valid() {
			t.Fatal("failed decode replaced valid request")
		}
	}
}
