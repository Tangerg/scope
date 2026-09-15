package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestExplicitRefusalCommitsExactPublicOutputBeforeModelContinuation(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%v", wrapped), func(t *testing.T) {
			var executions atomic.Int32
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "inspect"}, func(context.Context, struct{}) (string, error) {
				executions.Add(1)
				return "executed", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			guard, err := tool.NewGuard(tool.GuardConfig{
				Tool: executable, Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) { return false, nil }),
			})
			if err != nil {
				t.Fatal(err)
			}
			binding, err := tool.Bind(guard)
			if err != nil {
				t.Fatal(err)
			}
			output := chat.ToolOutput{
				Content: []chat.ToolContent{{Kind: chat.PartText, Text: "requested operation is not permitted"}},
				Details: json.RawMessage(`{"permitted":false}`),
			}
			public := &callbackTool{name: "inspect", call: func(ctx context.Context, arguments string) (string, error) {
				invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "current", Name: "inspect", Arguments: arguments})
				if err != nil {
					return "", err
				}
				_, denied := binding.Call(ctx, invocation)
				if denied == nil {
					return "", errors.New("expected refusal")
				}
				failure, err := tool.NewFailure(tool.FailureConfig{
					Kind: tool.FailureKindRejected, Output: output, Cause: errors.Join(context.DeadlineExceeded, denied),
				})
				if err != nil {
					return "", err
				}
				if wrapped {
					return "", fmt.Errorf("invocation: %w", failure)
				}
				return "", failure
			}}
			var committed []interaction.ResultEntry
			committer := &resultCommitter{commit: func(_ context.Context, batch interaction.ResultBatch) (interaction.ResultReceipt, error) {
				committed = append(committed, batch.Entries()...)
				return batch.Receipt(), nil
			}}
			modelCalls := 0
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				modelCalls++
				if modelCalls == 1 {
					return toolCallResponse(chat.ToolCall{ID: "current", Name: "inspect", Arguments: `{}`}), nil
				}
				want := chat.ToolResult{ID: "current", Name: "inspect", Output: output, IsError: true}
				if len(committed) != 1 || committed[0].Disposition != interaction.ResultRejected || !reflect.DeepEqual(committed[0].Result, want) {
					return nil, fmt.Errorf("incorrect committed refusal: %+v", committed)
				}
				matches := 0
				for _, message := range request.Messages {
					for _, part := range message.Parts {
						if part.ToolResult != nil && part.ToolResult.ID == "current" {
							matches++
							if !reflect.DeepEqual(*part.ToolResult, want) {
								return nil, errors.New("model refusal differs from publication")
							}
						}
					}
				}
				if matches != 1 {
					return nil, fmt.Errorf("model received %d refusal results", matches)
				}
				return textResponse("done"), nil
			})
			deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "authorization.output", Description: "Publish explicit refusal output.", MaxModelCalls: 2}, interaction.DispatcherConfig{Model: model, ResultCommitter: committer}, interaction.ToolSetConfig{Tools: []tool.Tool{public}})
			result := runInteraction(t, deployment, "work")
			if result.Status() != agent.StatusCompleted || executions.Load() != 0 || modelCalls != 2 {
				t.Fatalf("status=%s executions=%d model calls=%d", result.Status(), executions.Load(), modelCalls)
			}
		})
	}
}
