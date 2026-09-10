package interaction_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolBatchValidatesEveryProposalBeforeCapabilitiesAndExecution(t *testing.T) {
	var validCalls atomic.Int32
	var validCapabilities atomic.Int32
	var invalidCalls atomic.Int32
	var invalidCapabilities atomic.Int32
	valid := &trustBoundaryTool{
		name: "valid", calls: &validCalls, capabilities: &validCapabilities,
	}
	invalid := &trustBoundaryTool{
		name: "invalid", calls: &invalidCalls, capabilities: &invalidCapabilities,
	}
	var modelCalls atomic.Int32
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		switch modelCalls.Add(1) {
		case 1:
			return toolBatchResponse(
				chat.ToolCall{ID: "call_valid", Name: "valid", Arguments: `{}`},
				chat.ToolCall{ID: "call_invalid", Name: "invalid", Arguments: `{"unexpected":true}`},
			), nil
		case 2:
			message := request.Messages[len(request.Messages)-1]
			if message.Role != chat.RoleTool || len(message.Parts) != 2 {
				return nil, errors.New("model did not receive the complete Tool result batch")
			}
			first, second := message.Parts[0].ToolResult, message.Parts[1].ToolResult
			firstText, firstTextOK := toolResultText(first)
			secondText, secondTextOK := toolResultText(second)
			if first == nil || !firstTextOK || first.IsError || firstText != "valid result" {
				return nil, errors.New("valid Tool result was not preserved")
			}
			if second == nil || !secondTextOK || !second.IsError || !strings.Contains(secondText, "invalid arguments") {
				return nil, errors.New("invalid Tool proposal was not rejected deterministically")
			}
			return textResponse("done"), nil
		default:
			return nil, errors.New("unexpected model call")
		}
	})
	process, engine := startConcurrentInteraction(t, model, []tool.Tool{valid, invalid}, 2)
	result, err := process.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s", result.Status())
	}
	// Pure scheduling declarations may be checked again during state admission.
	if validCapabilities.Load() == 0 || validCalls.Load() != 1 {
		t.Fatalf("valid capability/calls = %d/%d, want a checked declaration and exactly one execution", validCapabilities.Load(), validCalls.Load())
	}
	if invalidCapabilities.Load() != 0 || invalidCalls.Load() != 0 {
		t.Fatalf("invalid capability/calls = %d/%d, want 0/0", invalidCapabilities.Load(), invalidCalls.Load())
	}
}

func TestLengthTruncatedToolCallsNeverReachCapabilitiesOrExecution(t *testing.T) {
	var calls atomic.Int32
	var capabilities atomic.Int32
	executable := &trustBoundaryTool{name: "inspect", calls: &calls, capabilities: &capabilities}
	var modelCalls atomic.Int32
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		switch modelCalls.Add(1) {
		case 1:
			message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
				ID: "call_truncated", Name: "inspect", Arguments: `{}`,
			}))
			return &chat.Response{Output: &chat.Output{
				Message: &message, FinishReason: chat.FinishReasonLength,
			}}, nil
		case 2:
			message := request.Messages[len(request.Messages)-1]
			if message.Role != chat.RoleTool || len(message.Parts) != 1 {
				return nil, errors.New("truncated Tool proposal did not produce model feedback")
			}
			result := message.Parts[0].ToolResult
			text, ok := toolResultText(result)
			if result == nil || !ok || !result.IsError || !strings.Contains(text, "token limit") {
				return nil, errors.New("truncated Tool proposal feedback is invalid")
			}
			return textResponse("done"), nil
		default:
			return nil, errors.New("unexpected model call")
		}
	})
	process, engine := startConcurrentInteraction(t, model, []tool.Tool{executable}, 2)
	result, err := process.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s", result.Status())
	}
	if capabilities.Load() != 0 || calls.Load() != 0 {
		t.Fatalf("capability/calls = %d/%d, want 0/0", capabilities.Load(), calls.Load())
	}
}

func TestNonToolCompletionNeverReachesCapabilitiesOrExecution(t *testing.T) {
	for _, reason := range []chat.FinishReason{chat.FinishReasonStop, chat.FinishReasonContentFilter, chat.FinishReasonRefusal, chat.FinishReasonOther} {
		t.Run(reason.String(), func(t *testing.T) {
			var calls, capabilities, modelCalls atomic.Int32
			executable := &trustBoundaryTool{name: "inspect", calls: &calls, capabilities: &capabilities}
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				if modelCalls.Add(1) != 1 {
					return textResponse("done"), nil
				}
				response := toolCallResponse(chat.ToolCall{ID: "call-1", Name: "inspect", Arguments: `{}`})
				response.Output.FinishReason = reason
				return response, nil
			})
			process, engine := startConcurrentInteraction(t, model, []tool.Tool{executable}, 2)
			result, err := process.Await(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
			failure, present := result.Termination().Failure()
			if result.Status() != agent.StatusFailed || !present || failure.Kind() != agent.FailureKindExternal || failure.Code() != "interaction.model.tool_calls_not_completed" {
				t.Fatalf("status=%s failure=%v", result.Status(), failure)
			}
			if calls.Load() != 0 || capabilities.Load() != 0 || modelCalls.Load() != 1 {
				t.Fatalf("tool calls=%d capabilities=%d model calls=%d", calls.Load(), capabilities.Load(), modelCalls.Load())
			}
		})
	}
}

func TestAuthorizationUsesManagedInvocationWithoutLeakingPolicyCause(t *testing.T) {
	const policySecret = "workspace internal role billing-admin"
	var calls atomic.Int32
	var authorizations atomic.Int32
	executable := &trustBoundaryTool{name: "inspect", calls: &calls, capabilities: new(atomic.Int32)}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(ctx context.Context, authorization tool.Authorization) error {
			authorizations.Add(1)
			invocation, present := interaction.ToolInvocationFromContext(ctx)
			if !present || invocation.ToolCall().ID != "call_denied" ||
				invocation.ToolCall().Name != authorization.Definition().Name {
				return errors.New("managed invocation attribution is unavailable")
			}
			return errors.New(policySecret)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls atomic.Int32
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		switch modelCalls.Add(1) {
		case 1:
			return toolCallResponse(chat.ToolCall{ID: "call_denied", Name: "inspect", Arguments: `{}`}), nil
		case 2:
			message := request.Messages[len(request.Messages)-1]
			if message.Role != chat.RoleTool || len(message.Parts) != 1 {
				return nil, errors.New("authorization denial did not produce Tool feedback")
			}
			result := message.Parts[0].ToolResult
			text, ok := toolResultText(result)
			if result == nil || !ok || !result.IsError ||
				!strings.Contains(text, "not authorized") || strings.Contains(text, policySecret) {
				return nil, errors.New("authorization feedback leaked policy details")
			}
			return textResponse("done"), nil
		default:
			return nil, errors.New("unexpected model call")
		}
	})
	process, engine := startConcurrentInteraction(t, model, []tool.Tool{guard}, 2)
	result, err := process.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted || authorizations.Load() != 1 || calls.Load() != 0 {
		t.Fatalf(
			"status = %s, authorizations = %d, tool calls = %d",
			result.Status(), authorizations.Load(), calls.Load(),
		)
	}
}

type trustBoundaryTool struct {
	name         string
	calls        *atomic.Int32
	capabilities *atomic.Int32
}

func (t *trustBoundaryTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name:        t.name,
		InputSchema: []byte(`{"type":"object","additionalProperties":false}`),
	}
}

func (t *trustBoundaryTool) Call(context.Context, tool.Invocation) (chat.ToolOutput, error) {
	t.calls.Add(1)
	return chat.NewTextToolOutput(t.name + " result"), nil
}

func (t *trustBoundaryTool) ConcurrencyKey(tool.Invocation) (string, bool) {
	t.capabilities.Add(1)
	return t.name, true
}

var _ interaction.ConcurrentTool = (*trustBoundaryTool)(nil)
