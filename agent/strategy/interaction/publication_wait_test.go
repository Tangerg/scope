package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestPublicationAcrossWaitingCheckpointAndStorageReopen(t *testing.T) {
	store := newPublicationStore(t)
	parked := make(chan struct{})
	var once sync.Once
	loss := errors.New("parked checkpoint acknowledgment lost")
	store.after = func(snapshot agent.TreeSnapshot, _ []interaction.RoundResults) error {
		inputs, err := interaction.PendingToolInputs(snapshot)
		if err != nil {
			return err
		}
		if len(inputs) == 1 {
			once.Do(func() { close(parked) })
			return loss
		}
		return nil
	}
	var executions, models atomic.Int32
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "confirm", Description: "Wait for input."}, func(ctx context.Context, _ struct{}) (string, error) {
		if _, resumed := interaction.ToolInputContinuationFromContext(ctx); !resumed {
			return "", interaction.RequireToolInput(json.RawMessage(`"continue?"`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{}`))
		}
		executions.Add(1)
		return "confirmed after input", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "publication.wait", Description: "Restore input without manufacturing a result."}, interaction.DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		if models.Add(1) == 1 {
			return toolCallResponse(chat.ToolCall{ID: "input", Name: "confirm", Arguments: `{}`}), nil
		}
		return textResponse("done"), nil
	})}, interaction.ToolSetConfig{Tools: []tool.Tool{executable}})
	engine := publicationEngine(t, deployment, store)
	root, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "work"))
	if err != nil {
		t.Fatal(err)
	}
	<-parked
	if joinErr := root.Join(t.Context()); !errors.Is(joinErr, loss) {
		t.Fatalf("join=%v", joinErr)
	}
	if len(store.entries()) != 0 || executions.Load() != 0 {
		t.Fatal("input checkpoint became an execution result")
	}
	path := store.path
	store.Close()
	store = openPublicationStore(t, path)
	restoredEngine := publicationEngine(t, deployment, store)
	restored, err := restoredEngine.RestoreTree(t.Context(), deployment.Deployment, store.tree())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := interaction.PendingToolInputs(store.tree())
	if err != nil || len(inputs) != 1 {
		t.Fatalf("inputs=%v err=%v", inputs, err)
	}
	id, err := agent.ParseSignalID("signal:answer")
	if err != nil {
		t.Fatal(err)
	}
	signal, err := inputs[0].ResponseSignal(id, json.RawMessage(`true`))
	if err != nil {
		t.Fatal(err)
	}
	child, found := restoredEngine.Process(inputs[0].ProcessID())
	if !found {
		t.Fatal("missing input child")
	}
	if _, deliverErr := child.DeliverSignals(t.Context(), signal); deliverErr != nil {
		t.Fatal(deliverErr)
	}
	if joinErr := restored.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	result, err := restored.Await(t.Context())
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%s err=%v", result.Status(), err)
	}
	entries := store.entries()
	if len(entries) != 1 || publicationText(entries[0]) != "confirmed after input" || executions.Load() != 1 || models.Load() != 2 {
		t.Fatalf("results=%v executions=%d models=%d", entries, executions.Load(), models.Load())
	}
}
