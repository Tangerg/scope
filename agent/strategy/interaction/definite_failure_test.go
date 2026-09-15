package interaction_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestDefiniteToolFailureSurvivesCancellationCauseAndTreeRestore(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			output := chat.NewTextToolOutput("first write completed; second write did not start")
			failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: cause, Output: output})
			if err != nil {
				t.Fatal(err)
			}
			var toolCalls, modelCalls atomic.Int32
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "partial", Description: "Report a definite partial outcome."}, func(context.Context, struct{}) (string, error) {
				toolCalls.Add(1)
				return "", failure
			})
			if err != nil {
				t.Fatal(err)
			}
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				if modelCalls.Add(1) == 1 {
					message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "partial_call", Name: "partial", Arguments: `{}`}))
					return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}, nil
				}
				result := request.Messages[len(request.Messages)-1].Parts[0].ToolResult
				if result == nil || !result.IsError || !reflect.DeepEqual(result.Output, output) {
					return nil, errors.New("definite output was not delivered to the model")
				}
				return textResponse("accounted for"), nil
			})
			deployment := newDeployment(t, model, []tool.Tool{executable}, 2)
			engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(context.WithoutCancel(ctx))
			result, err := engine.Run(ctx, deployment.Deployment, interactionInput(t, "run"))
			if err != nil || result.Status() != agent.StatusCompleted || modelCalls.Load() != 2 {
				t.Fatalf("result=%+v error=%v model calls=%d", result, err, modelCalls.Load())
			}
			snapshot, err := engine.CaptureTree(ctx, result.ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			for _, process := range snapshot.ProcessSnapshots() {
				if len(process.UnknownEffectIDs()) != 0 {
					t.Fatal("definite failure created an unknown Effect")
				}
			}
			restoredEngine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
			if err != nil {
				t.Fatal(err)
			}
			defer restoredEngine.Close(context.WithoutCancel(ctx))
			restored, err := restoredEngine.RestoreTree(ctx, deployment.Deployment, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			restoredResult, err := restored.Await(ctx)
			if err != nil || restoredResult.Status() != agent.StatusCompleted || toolCalls.Load() != 1 || modelCalls.Load() != 2 {
				t.Fatalf("restored=%+v error=%v tool calls=%d model calls=%d", restoredResult, err, toolCalls.Load(), modelCalls.Load())
			}
		})
	}
}

func TestProcessCancellationRetainsDefiniteToolSettlement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "cancel_after_write", Description: "Report a known outcome after cancellation."}, func(ctx context.Context, _ struct{}) (string, error) {
		close(started)
		<-ctx.Done()
		failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: ctx.Err(), Output: chat.NewTextToolOutput("write completed before cancellation")})
		if err != nil {
			return "", err
		}
		return "", failure
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &singleToolCallModel{call: chat.ToolCall{ID: "cancel_call", Name: "cancel_after_write", Arguments: `{}`}}
	deployment := newDeployment(t, model, []tool.Tool{executable}, 2)
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.WithoutCancel(ctx))
	process, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "cancel after execution starts"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if cancelErr := process.RequestCancellation(ctx, "stop the process"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	result, err := process.Await(ctx)
	if err != nil || result.Status() != agent.StatusCanceled {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if joinErr := process.Join(ctx); joinErr != nil {
		t.Fatal(joinErr)
	}
	snapshot, err := engine.CaptureTree(ctx, process.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range snapshot.ProcessSnapshots() {
		if len(child.UnknownEffectIDs()) != 0 {
			t.Fatal("cancellation erased a definite tool settlement")
		}
	}
}
