package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type referenceObserver struct {
	started chan interaction.ToolInvocation
	settled chan interaction.ToolInvocation
}

func (r *referenceObserver) OnToolStarted(_ context.Context, invocation interaction.ToolInvocation) {
	r.started <- invocation
}

func (r *referenceObserver) OnToolSettled(_ context.Context, invocation interaction.ToolInvocation, _ interaction.ToolSettlement) {
	r.settled <- invocation
}

func TestLogicalReferenceAcrossAttemptsRoundsObserversAndParents(t *testing.T) {
	observer := &referenceObserver{started: make(chan interaction.ToolInvocation, 3), settled: make(chan interaction.ToolInvocation, 3)}
	invoked := make(chan interaction.ToolInvocation, 3)
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "confirm", Description: "Confirm one logical call."},
		func(ctx context.Context, _ struct{}) (string, error) {
			invocation, found := interaction.ToolInvocationFromContext(ctx)
			if !found {
				return "", errors.New("Tool invocation is missing")
			}
			invoked <- invocation
			reference, _ := invocation.Reference()
			if _, resumed := interaction.ToolInputContinuationFromContext(ctx); reference.ModelCallSequence() == 1 && !resumed {
				return "", interaction.RequireToolInput(json.RawMessage(`"continue?"`), json.RawMessage(`{"const":true}`), json.RawMessage(`null`))
			}
			return "confirmed", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
		invocation, found := interaction.ModelInvocationFromContext(ctx)
		if !found {
			return nil, errors.New("model invocation is missing")
		}
		if invocation.ModelCallSequence() > 2 {
			return textResponse("done"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: "provider-reused", Name: "confirm", Arguments: `{}`}), nil
	})
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "reference.lifecycle", Description: "Correlate logical calls."},
		interaction.DispatcherConfig{Model: model}, interaction.ToolSetConfig{Tools: []tool.Tool{executable}, Observer: observer})
	references := make(map[interaction.ToolCallRef]struct{})
	for range 2 {
		store := newPublicationStore(t)
		engine := publicationEngine(t, deployment, store)
		root, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "confirm"))
		if err != nil {
			t.Fatal(err)
		}
		_, pending := captureToolInput(t, engine, root)
		id, err := agent.ParseSignalID("signal:confirmation")
		if err != nil {
			t.Fatal(err)
		}
		answer, err := pending.ResponseSignal(id, json.RawMessage(`true`))
		if err != nil {
			t.Fatal(err)
		}
		child := pendingToolProcess(t, engine, pending)
		if accepted, err := child.DeliverSignals(t.Context(), answer); err != nil || !accepted {
			t.Fatalf("input answer accepted=%v, error=%v", accepted, err)
		}
		if result, err := root.Await(t.Context()); err != nil || result.Status() != agent.StatusCompleted {
			t.Fatalf("completion=%s, error=%v", result.Status(), err)
		}
		if err := root.Join(t.Context()); err != nil {
			t.Fatal(err)
		}
		var previous interaction.ToolCallRef
		var previousAttempt agent.EffectAttemptID
		for index, sequence := range []uint64{1, 1, 2} {
			invocation := <-invoked
			started, settled := <-observer.started, <-observer.settled
			reference, present := invocation.Reference()
			startedReference, startedPresent := started.Reference()
			settledReference, settledPresent := settled.Reference()
			if !present || !startedPresent || !settledPresent || reference != startedReference || reference != settledReference ||
				reference.ProcessID() != root.ID() || reference.ModelCallSequence() != sequence || reference.ToolCallIndex() != 0 || invocation.ToolCall().ID != "provider-reused" {
				t.Fatalf("attempt %d reference=%v, started=%v, settled=%v", index, reference, startedReference, settledReference)
			}
			attempt, dispatched := invocation.AttemptID()
			if !dispatched || attempt == previousAttempt {
				t.Fatal("physical attempts were not distinct")
			}
			if index == 1 && reference != previous {
				t.Fatal("input continuation changed the logical call")
			}
			if index != 1 {
				if _, collision := references[reference]; collision {
					t.Fatal("provider ID reuse or a different parent collided")
				}
				references[reference] = struct{}{}
			}
			previous, previousAttempt = reference, attempt
		}
		path := store.path
		store.Close()
		restored := openPublicationStore(t, path)
		if len(restored.database.Results) != 2 {
			t.Fatalf("stored logical results=%d, want 2", len(restored.database.Results))
		}
		for _, result := range restored.database.Results {
			if _, found := references[result.Reference]; !found || result.Reference.ProcessID() != root.ID() {
				t.Fatalf("restored result has a different reference: %v", result.Reference)
			}
		}
		restored.Close()
	}
	if len(references) != 4 {
		t.Fatalf("logical calls=%d, want 4", len(references))
	}
}
