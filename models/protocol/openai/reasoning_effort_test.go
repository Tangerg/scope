package openai

import (
	"strings"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestChatMapsCoreReasoningEffort(t *testing.T) {
	request := validReasoningRequest(t, "high")
	model := &ChatCompletions{
		api:      &api{},
		defaults: corechat.Options{Model: "gpt-5"},
		dialect:  Dialect{Provider: "openai", TokenLimitField: TokenLimitMaxCompletionTokens},
	}
	params, err := model.buildRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if params.ReasoningEffort != openaisdk.ReasoningEffortHigh {
		t.Fatalf("reasoning_effort = %q", params.ReasoningEffort)
	}

	if err := request.Options.Extensions.Set(RequestExtensionKey, map[string]any{"reasoning_effort": "low"}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.buildRequest(request); err == nil || !strings.Contains(err.Error(), "owned by Core") {
		t.Fatalf("duplicate owner error = %v", err)
	}
}

func TestResponsesMapsCoreReasoningEffortAndTokenProjection(t *testing.T) {
	request := validReasoningRequest(t, "xhigh")
	model := &Responses{api: &api{}, defaults: corechat.Options{Model: "gpt-5"}}
	params, err := model.buildResponsesRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if params.Reasoning.Effort != openaisdk.ReasoningEffortXhigh {
		t.Fatalf("reasoning.effort = %q", params.Reasoning.Effort)
	}
	projected, err := projectResponsesInputTokenCount(params)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Reasoning.Effort != openaisdk.ReasoningEffortXhigh {
		t.Fatalf("projected reasoning.effort = %q", projected.Reasoning.Effort)
	}
	if err := request.Options.Extensions.Set(ResponsesRequestExtensionKey, map[string]any{
		"reasoning": map[string]any{"effort": "low"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.buildResponsesRequest(request); err == nil || !strings.Contains(err.Error(), "owned by options.reasoning_effort") {
		t.Fatalf("duplicate owner error = %v", err)
	}
}

func TestReasoningEffortRejectsUnknownProviderValue(t *testing.T) {
	request := validReasoningRequest(t, "turbo")
	model := &Responses{api: &api{}, defaults: corechat.Options{Model: "gpt-5"}}
	if _, err := model.buildResponsesRequest(request); err == nil || !strings.Contains(err.Error(), "unsupported value") {
		t.Fatalf("error = %v", err)
	}
}

func validReasoningRequest(t *testing.T, effort corechat.ReasoningEffort) *corechat.Request {
	t.Helper()
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatal(err)
	}
	request.Options.ReasoningEffort = effort
	return request
}
