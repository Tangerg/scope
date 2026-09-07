package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolInputRequestPreservesJSONNumbers(t *testing.T) {
	for _, value := range []string{"9007199254740993", "18446744073709551615", "1.234567890123456789", "1e400"} {
		t.Run(value, func(t *testing.T) {
			schema := `{"const":` + value + `}`
			request, err := interaction.NewToolInputRequest(json.RawMessage(value), json.RawMessage(schema), json.RawMessage(value))
			if err != nil {
				t.Fatal(err)
			}
			if string(request.Prompt()) != value || string(request.ResponseSchema()) != schema || string(request.ContinuationState()) != value {
				t.Fatalf("request changed JSON numbers: prompt=%s schema=%s continuation=%s", request.Prompt(), request.ResponseSchema(), request.ContinuationState())
			}
		})
	}
}

func TestToolInputRequestRejectsInvalidAndOversizedJSON(t *testing.T) {
	for name, value := range map[string]string{
		"duplicate member": `{"answer":1,"answer":2}`,
		"trailing value":   `1 2`,
		"invalid unicode":  `"\ud800"`,
		"normalized size":  `"` + strings.Repeat("<", (1<<20)/6+1) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := interaction.NewToolInputRequest(json.RawMessage(value), json.RawMessage(`true`), json.RawMessage(`null`))
			if !errors.Is(err, interaction.ErrInvalidToolInputRequest) {
				t.Fatalf("error = %v, want ErrInvalidToolInputRequest", err)
			}
		})
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
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	process, err := engine.Start(ctx, deployment, interactionInput(t, "confirm"))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if process != nil {
			if _, awaitErr := process.Await(cleanup); awaitErr != nil {
				t.Error(awaitErr)
			}
		}
		if closeErr := engine.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, agent.StatusWaiting)
	tree, err := engine.CaptureTree(ctx, process.ID())
	if err != nil {
		t.Fatal(err)
	}
	tree, err = agent.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if killErr := process.Kill(ctx, "restore numeric input checkpoint"); killErr != nil {
		t.Fatal(killErr)
	}
	if _, awaitErr := process.Await(ctx); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	if releaseErr := engine.ReleaseTree(ctx, process.ID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	process, err = engine.RestoreTree(ctx, deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	pending, found, err := interaction.PendingToolInputFromProcess(ctx, process)
	if err != nil || !found {
		t.Fatalf("pending found=%t error=%v", found, err)
	}
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
	if accepted, deliveryErr := process.DeliverSignals(ctx, signal); deliveryErr != nil || !accepted {
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
