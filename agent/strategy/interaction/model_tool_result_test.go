package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestModelToolResultPolicy(t *testing.T) {
	call := chat.ToolCall{ID: "call", Name: "feedback", Arguments: `{}`}
	structured, err := chat.NewJSONToolOutput(json.RawMessage(`{"value":42}`))
	if err != nil {
		t.Fatal(err)
	}
	success, resultErr := interaction.ModelToolResult(call, structured, nil)
	wantSuccess := chat.ToolResult{ID: call.ID, Name: call.Name, Output: structured}
	if resultErr != nil || !reflect.DeepEqual(success, wantSuccess) {
		t.Fatalf("success = %#v, error = %v", success, resultErr)
	}
	empty, resultErr := interaction.ModelToolResult(call, chat.ToolOutput{}, nil)
	if resultErr != nil || !reflect.DeepEqual(empty, chat.ToolResult{ID: call.ID, Name: call.Name}) {
		t.Fatalf("empty success = %#v, error = %v", empty, resultErr)
	}
	invalidOutput := chat.ToolOutput{Details: json.RawMessage(`{`)}
	invalid, resultErr := interaction.ModelToolResult(call, invalidOutput, nil)
	wantInvalid := chat.ToolResult{
		ID: call.ID, Name: call.Name, IsError: true,
		Output: chat.NewTextToolOutput("error: tool \"" + call.Name +
			"\" failed: tool returned invalid output: chat: invalid tool output: details must be one valid RFC 7493 JSON document"),
	}
	if resultErr != nil || !reflect.DeepEqual(invalid, wantInvalid) {
		t.Fatalf("invalid output result = %#v, error = %v", invalid, resultErr)
	}

	diagnostic := strings.Repeat("x", 3_000)
	failure, resultErr := interaction.ModelToolResult(call, chat.NewTextToolOutput("ignored"), errors.New(diagnostic))
	wantFailure := chat.ToolResult{
		ID: call.ID, Name: call.Name,
		Output:  chat.NewTextToolOutput("error: tool \"" + call.Name + "\" failed: " + diagnostic[:2_048]),
		IsError: true,
	}
	if resultErr != nil || !reflect.DeepEqual(failure, wantFailure) {
		t.Fatalf("failure = %#v, error = %v", failure, resultErr)
	}
	completeFailure, err := tool.NewFailure(errors.New("partial execution"), structured)
	if err != nil {
		t.Fatal(err)
	}
	complete, resultErr := interaction.ModelToolResult(call, chat.ToolOutput{}, fmt.Errorf("wrapped: %w", completeFailure))
	if resultErr != nil || !reflect.DeepEqual(complete, chat.ToolResult{ID: call.ID, Name: call.Name, IsError: true, Output: structured}) {
		t.Fatalf("complete failure = %#v, error = %v", complete, resultErr)
	}

	controlCauses := []error{
		interaction.HostFailure(errors.New("projection unavailable")),
		context.Canceled,
		context.DeadlineExceeded,
		interaction.RequireToolInput(
			json.RawMessage(`"continue?"`),
			json.RawMessage(`{"type":"boolean"}`),
			json.RawMessage(`{"stage":"waiting"}`),
		),
	}
	for _, cause := range controlCauses {
		if result, resultErr := interaction.ModelToolResult(call, invalidOutput, cause); !errors.Is(resultErr, cause) || !reflect.DeepEqual(result, chat.ToolResult{}) {
			t.Fatalf("control cause %v produced %#v, error = %v", cause, result, resultErr)
		}
	}
	if result, resultErr := interaction.ModelToolResult(chat.ToolCall{}, chat.NewTextToolOutput("ignored"), nil); resultErr == nil || !reflect.DeepEqual(result, chat.ToolResult{}) {
		t.Fatalf("invalid call produced %#v, error = %v", result, resultErr)
	}
	authorizationCause := fmt.Errorf("private authorization detail: %w", tool.ErrAuthorizationDenied)
	denied, err := interaction.ModelToolResult(call, structured, authorizationCause)
	wantDenied := chat.ToolResult{ID: call.ID, Name: call.Name, IsError: true,
		Output: chat.NewTextToolOutput("error: tool \"feedback\" is not authorized")}
	if err != nil || !reflect.DeepEqual(denied, wantDenied) {
		t.Fatalf("authorization result=%+v error=%v", denied, err)
	}
	structured.Details[0] = '['
	if string(success.Output.Details) != `{"value":42}` || string(complete.Output.Details) != `{"value":42}` {
		t.Fatal("model feedback retained caller-owned output")
	}
}
