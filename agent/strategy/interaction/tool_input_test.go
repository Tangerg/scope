package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestRequireToolInputSupportsErrorClassification(t *testing.T) {
	err := interaction.RequireToolInput(
		json.RawMessage(`"continue?"`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{"step":2}`),
	)
	if !errors.Is(fmt.Errorf("tool paused: %w", err), interaction.ErrToolInputRequired) {
		t.Fatalf("wrapped input request = %v, want ErrToolInputRequired", err)
	}
}

func TestRequireToolInputRejectsInvalidAndOversizedJSON(t *testing.T) {
	for name, value := range map[string]string{
		"duplicate member": `{"answer":1,"answer":2}`,
		"trailing value":   `1 2`,
		"invalid unicode":  `"\ud800"`,
		"normalized size":  `"` + strings.Repeat("<", (1<<20)/6+1) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			err := interaction.RequireToolInput(json.RawMessage(value), json.RawMessage(`true`), json.RawMessage(`null`))
			if !errors.Is(err, interaction.ErrInvalidToolInputRequest) {
				t.Fatalf("error = %v, want ErrInvalidToolInputRequest", err)
			}
		})
	}
}

func TestPendingToolInputsTracksPausedWaitUntilAnswered(t *testing.T) {
	waiting := newInputRequestTool()
	waiting.Release()
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if request.Messages[len(request.Messages)-1].Role == chat.RoleTool {
			return textResponse("done"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: "ask", Name: "ask_name", Arguments: `{}`}), nil
	})
	deployment := newDeployment(t, model, []tool.Tool{waiting}, 2)
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "greet"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer stop()
		if killErr := root.Kill(cleanup, "test complete"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) {
			t.Error(killErr)
		}
		if joinErr := root.Join(cleanup); joinErr != nil {
			t.Error(joinErr)
		}
	})
	_, pending := captureToolInput(t, engine, root)
	child := pendingToolProcess(t, engine, pending)
	if pauseErr := child.Pause(ctx, "hold input continuation"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	waitForStatus(t, engine, child, agent.StatusPaused)
	tree, err := engine.CaptureTree(ctx, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	tree, err = agent.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := interaction.PendingToolInputs(tree)
	if err != nil || len(inputs) != 1 {
		t.Fatalf("paused Tool inputs=%v error=%v", inputs, err)
	}
	if inputs[0].ProcessID() != child.ID() || inputs[0].WaitID() != pending.WaitID() ||
		string(inputs[0].Prompt()) != string(pending.Prompt()) || string(inputs[0].ResponseSchema()) != string(pending.ResponseSchema()) {
		t.Fatal("pause changed the pending Tool input")
	}
	id, err := agent.ParseSignalID("signal:paused-tool-answer")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := inputs[0].ResponseSignal(id, json.RawMessage(`"Ada"`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := child.DeliverSignals(ctx, answer); deliveryErr != nil || !accepted {
		t.Fatalf("paused Tool answer=%t error=%v", accepted, deliveryErr)
	}
	tree, err = engine.CaptureTree(ctx, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err = interaction.PendingToolInputs(tree)
	if err != nil || len(inputs) != 0 {
		t.Fatalf("answered Tool inputs=%v error=%v", inputs, err)
	}
	if inspectProcessSnapshot(t, engine, child).Status() != agent.StatusPaused || waiting.continuationCalls.Load() != 0 {
		t.Fatal("answer released the Tool pause")
	}
	if resumeErr := child.Resume(ctx); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	result, err := root.Await(ctx)
	if err != nil || result.Status() != agent.StatusCompleted || waiting.initialCalls.Load() != 1 || waiting.continuationCalls.Load() != 1 {
		t.Fatalf("result=%s error=%v Tool calls=%d/%d", result.Status(), err, waiting.initialCalls.Load(), waiting.continuationCalls.Load())
	}
}

func TestToolInputResponsePreservesNumbersAcrossRestore(t *testing.T) {
	for _, sample := range []struct {
		name     string
		facade   bool
		response string
	}{
		{name: "primitive", response: "9007199254740993"},
		{name: "facade", facade: true, response: "9007199254740993"},
		{name: "invalid primitive response", response: "9007199254740992"},
	} {
		t.Run(sample.name, func(t *testing.T) { testNumericToolInputRestore(t, sample.facade, sample.response) })
	}
}

func testNumericToolInputRestore(t *testing.T, facade bool, response string) {
	t.Helper()
	const value = "9007199254740993"
	const schema = `{"const":9007199254740993}`
	resumed := make(chan interaction.ToolInputContinuation, 1)
	executable, err := tool.NewFunc(tool.FuncConfig{
		Name: "confirm_number", Description: "Confirm an exact numeric value.",
	}, func(ctx context.Context, _ struct{}) (string, error) {
		continuation, ok := interaction.ToolInputContinuationFromContext(ctx)
		if !ok {
			return "", interaction.RequireToolInput(json.RawMessage(value), json.RawMessage(schema), json.RawMessage(value))
		}
		resumed <- continuation
		return "confirmed", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if request.Messages[len(request.Messages)-1].Role == chat.RoleTool {
			return textResponse("confirmed"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: "confirm", Name: "confirm_number", Arguments: `{}`}), nil
	})
	deployment := newDeployment(t, model, []tool.Tool{executable}, 2)
	store := agent.NewMemoryTreeCommitter()
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store, DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	process, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "confirm"))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if process != nil {
			if _, awaitErr := process.Await(cleanup); awaitErr != nil {
				t.Error(awaitErr)
			}
		}
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	tree, pending := captureToolInput(t, engine, process)
	tree, err = agent.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	oldEngine, oldProcess := engine, process
	engine, err = agent.NewEngine(agent.EngineConfig{TreeCommitter: store, DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { retireTestWriter(t, oldEngine, oldProcess) })
	process, err = engine.RestoreTree(ctx, deployment.Deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	toolProcess := pendingToolProcess(t, engine, pending)
	if string(pending.Prompt()) != value || string(pending.ResponseSchema()) != schema {
		t.Fatalf("restored prompt=%s schema=%s", pending.Prompt(), pending.ResponseSchema())
	}
	id, err := agent.ParseSignalID("signal:numeric-answer")
	if err != nil {
		t.Fatal(err)
	}
	if _, responseErr := pending.ResponseSignal(id, json.RawMessage(`9007199254740992`)); !errors.Is(responseErr, interaction.ErrInvalidToolInputRequest) {
		t.Fatalf("adjacent integer validation error=%v", responseErr)
	}
	var signal agent.SignalRequest
	if facade {
		signal, err = pending.ResponseSignal(id, json.RawMessage(response))
	} else {
		signal, err = interaction.NewToolInputResponseSignal(id, pending.WaitID(), json.RawMessage(response))
	}
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := toolProcess.DeliverSignals(ctx, signal); deliveryErr != nil || !accepted {
		t.Fatalf("response accepted=%t error=%v", accepted, deliveryErr)
	}
	result, err := process.Await(ctx)
	wantStatus := agent.StatusCompleted
	if response != value {
		wantStatus = agent.StatusFailed
	}
	if err != nil || result.Status() != wantStatus {
		t.Fatalf("result status=%s error=%v", result.Status(), err)
	}
	select {
	case continuation := <-resumed:
		if response != value {
			t.Fatal("tool resumed with an invalid response")
		}
		if string(continuation.State()) != value || string(continuation.Response()) != value {
			t.Fatalf("resumed state=%s response=%s", continuation.State(), continuation.Response())
		}
	default:
		if response == value {
			t.Fatal("tool did not resume")
		}
	}
}
