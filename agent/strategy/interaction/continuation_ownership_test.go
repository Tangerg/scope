package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestRestoredContinuationBelongsOnlyToItsLogicalCall(t *testing.T) {
	var requested atomic.Bool
	var freshCalls atomic.Int32
	var binding tool.Binding
	var nested func(context.Context) error
	executable, err := newContinuingTool(tool.FuncConfig{Name: "probe", Description: "Inspect one call's input."}, func(ctx context.Context, callInput tool.Invocation) (tool.Result, error) {
		continuation, resumed := callInput.Continuation()
		if !resumed {
			if requested.CompareAndSwap(false, true) {
				return waitingInput([]byte(`"confirm"`), []byte(`{"const":true}`), []byte(`{"owner":"outer"}`))
			}
			freshCalls.Add(1)
			return tool.Result{Output: chat.NewTextToolOutput("fresh")}, nil
		}
		if string(continuation.State()) != `{"owner":"outer"}` || string(continuation.Response()) != `true` {
			return tool.Result{}, errors.New("outer continuation changed")
		}
		invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "same", Name: "probe", Arguments: `{}`}, nil)
		if err != nil {
			return tool.Result{}, err
		}
		result, err := binding.Call(ctx, invocation)
		if err != nil {
			return tool.Result{}, err
		}
		if text, _ := result.Output.Text(); text != "fresh" {
			return tool.Result{}, errors.New("recursive call inherited outer input")
		}
		if err := nested(ctx); err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Output: chat.NewTextToolOutput("resumed")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err = tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if request.Messages[len(request.Messages)-1].Role == chat.RoleTool {
			return textResponse("done"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: "same", Name: "probe", Arguments: `{}`}), nil
	})
	deployment := newDeployment(t, model, []tool.Tool{executable}, 2)
	inner, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := inner.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	nested = func(ctx context.Context) error {
		result, runErr := inner.Run(ctx, deployment.Deployment, interactionInput(t, "nested"))
		if runErr != nil {
			return runErr
		}
		if result.Termination().Status() != agent.StatusCompleted {
			return errors.New("independent Engine inherited outer input")
		}
		return inner.ReleaseTree(ctx, result.ProcessID())
	}
	store := agent.NewMemoryTreeCommitter()
	original, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
	if err != nil {
		t.Fatal(err)
	}
	root, err := original.Start(t.Context(), deployment.Deployment, interactionInput(t, "outer"))
	if err != nil {
		t.Fatal(err)
	}
	tree, pending := captureToolInput(t, original, root)
	restored, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := restored.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	process, err := restored.RestoreTree(t.Context(), deployment.Deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	retireTestWriter(t, original, root)
	id, err := agent.ParseSignalID("signal:owned-answer")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := pending.ResponseSignal(id, json.RawMessage(`true`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := pendingToolProcess(t, restored, pending).DeliverSignals(t.Context(), answer); deliveryErr != nil || !accepted {
		t.Fatalf("accepted=%t error=%v", accepted, deliveryErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Termination().Status() != agent.StatusCompleted || freshCalls.Load() != 2 {
		t.Fatalf("status=%s error=%v fresh=%d", result.Termination().Status(), err, freshCalls.Load())
	}
}
