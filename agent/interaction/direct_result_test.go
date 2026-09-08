package interaction_test

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestDirectResultToolFailuresReturnToModel(t *testing.T) {
	for _, failedIndex := range []int{0, 1} {
		t.Run([]string{"first", "last"}[failedIndex], func(t *testing.T) {
			type input struct {
				Index int `json:"index"`
			}
			calls := 0
			executable, err := tool.NewFunc(tool.FuncConfig{
				Name: "direct", Description: "Return a direct result when execution succeeds.",
			}, func(_ context.Context, value input) (string, error) {
				calls++
				if value.Index == failedIndex {
					return "", errors.New("execution failed")
				}
				return "success", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			modelCalls := 0
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				modelCalls++
				if modelCalls == 1 {
					return toolBatchResponse(
						chat.ToolCall{ID: "first", Name: "direct", Arguments: `{"index":0}`},
						chat.ToolCall{ID: "last", Name: "direct", Arguments: `{"index":1}`},
					), nil
				}
				message := request.Messages[len(request.Messages)-1]
				if message.Role != chat.RoleTool || len(message.Parts) != 2 {
					return nil, errors.New("model did not receive the complete tool result batch")
				}
				first, last := message.Parts[0].ToolResult, message.Parts[1].ToolResult
				if first == nil || last == nil || first.ID != "first" || last.ID != "last" ||
					first.IsError != (failedIndex == 0) || last.IsError != (failedIndex == 1) {
					t.Errorf("tool results = %#v, want ordered success and failure", message.Parts)
				}
				return textResponse("handled failure"), nil
			})
			result := runInteraction(t, newDeployment(t, model, []tool.Tool{directTool{Tool: executable}}, 2), "run batch")
			if result.Status() != agent.StatusCompleted || modelCalls != 2 || calls != 2 {
				t.Fatalf("status = %s, model calls = %d, tool calls = %d", result.Status(), modelCalls, calls)
			}
			erased, _ := result.Output()
			output, err := erased.Decode[interaction.Output]()
			if err != nil {
				t.Fatal(err)
			}
			if output.Source != interaction.CompletionSourceModelResponse || output.ModelResponse.Text() != "handled failure" {
				t.Fatalf("output = %#v", output)
			}
		})
	}
}

func TestOutputRejectsUnfinishedCompletion(t *testing.T) {
	for name, output := range map[string]interaction.Output{
		"failed direct result": {
			Source: interaction.CompletionSourceDirectToolResults, ModelCalls: 1,
			DirectToolResults: []chat.ToolResult{{
				ID: "failed", Name: "direct", IsError: true, Output: chat.NewTextToolOutput("failure"),
			}},
		},
		"pending tool call": {
			Source: interaction.CompletionSourceModelResponse, ModelCalls: 1,
			ModelResponse: toolCallResponse(chat.ToolCall{ID: "pending", Name: "direct", Arguments: `{}`}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := output.Validate(); err == nil {
				t.Fatal("unfinished completion was accepted")
			}
		})
	}
}

func TestDirectResultCompletionFailurePreservesItsCause(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision interaction.CompletionDecision
		err      error
		kind     agent.FailureKind
		code     string
	}{
		{name: "validator error", err: errors.New("completion unavailable"), kind: agent.FailureKindExecution, code: "interaction.completion.validator_failed"},
		{name: "invalid decision", kind: agent.FailureKindContract, code: "interaction.completion.decision_invalid"},
		{name: "retry limit", decision: interaction.CompletionDecision{Feedback: "Explain the result."}, kind: agent.FailureKindExecution, code: "interaction.limit.model_calls"},
	} {
		t.Run(test.name, func(t *testing.T) {
			toolCalls := 0
			executable, err := tool.NewFunc(tool.FuncConfig{
				Name: "direct", Description: "Return the requested direct result.",
			}, func(context.Context, struct{}) (string, error) {
				toolCalls++
				return "result", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			modelCalls, validations := 0, 0
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				modelCalls++
				return toolCallResponse(chat.ToolCall{ID: "direct-1", Name: "direct", Arguments: `{}`}), nil
			})
			validator := func(candidate interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
				validations++
				if candidate.Output().Source != interaction.CompletionSourceDirectToolResults {
					t.Error("validator did not receive the direct Tool result")
				}
				return test.decision, test.err
			}
			deployment := delegateInteractionWithValidator(t, model, []tool.Tool{directTool{Tool: executable}}, nil, validator, 1)
			result := runInteraction(t, deployment, "validate direct result")
			failure, present := result.Termination().Failure()
			if result.Status() != agent.StatusFailed || !present || failure.Kind() != test.kind || failure.Code() != test.code {
				t.Fatalf("termination = %#v, want %s/%s", result.Termination(), test.kind, test.code)
			}
			if modelCalls != 1 || toolCalls != 1 || validations != 1 {
				t.Fatalf("model calls = %d, Tool calls = %d, validations = %d; want one each", modelCalls, toolCalls, validations)
			}
		})
	}
}
