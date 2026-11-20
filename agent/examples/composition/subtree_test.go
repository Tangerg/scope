package main

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

func unknownComposition(t *testing.T, runtime *agentrun.Runtime, calls *atomic.Int32) (agentrun.Program[compositionInput, compositionOutput], agentrun.Program[interaction.Input, interaction.Output]) {
	t.Helper()
	model := compositionValue(interaction.Compile(runtime.Compiler(), interaction.Config{Name: "test.uncertain_model", Description: "Preserve an unacknowledged model result.", Revision: "uncertain-v1", Model: interaction.CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls.Add(1)
		return nil, errors.New("response lost")
	}))}))
	local := compositionValue(newUppercaseProgram(runtime.Compiler()))
	return compositionValue(newCompositionProgram(runtime.Compiler(), local, model)), model
}

func awaitUnknown(t *testing.T, handle *agentrun.Handle[compositionOutput]) agentrun.PendingEffect {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		view, err := handle.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range view.Processes {
			for _, pending := range process.PendingEffects {
				if pending.Outcome.Status() == agentrun.SettlementStatusUnknown {
					return pending
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return agentrun.PendingEffect{}
		case <-ticker.C:
		}
	}
}

func TestUnknownChildSettlementSurvivesCompositionRecovery(t *testing.T) {
	store := agentrun.NewMemoryStore()
	runtime := conformancetest.Runtime(t, agentrun.Config{Store: store, CheckpointInterval: 1})
	var calls atomic.Int32
	program, model := unknownComposition(t, runtime, &calls)
	handle := compositionValue(runtime.Start(t.Context(), program, agentrun.StartRequest[compositionInput]{ID: agentrun.StartID{Scope: "test", Key: t.Name()}, Input: compositionInput{Prompt: "recover"}}))
	pending := awaitUnknown(t, handle)
	if pending.Request.ID().Process.Depth() != 2 {
		t.Fatal("model was not a nested child")
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	recoveredRuntime := conformancetest.Runtime(t, agentrun.Config{Store: store})
	recovered := compositionValue(recoveredRuntime.Recover(t.Context(), program, handle.ID()))
	restored := awaitUnknown(t, recovered)
	if restored.Request.ID() != pending.Request.ID() || !bytes.Equal(restored.Request.Effect().Payload().JSON(), pending.Request.Effect().Payload().JSON()) || restored.Request.ProgramRef() != pending.Request.ProgramRef() {
		t.Fatal("recovery changed committed effect intent")
	}
	response := &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("reconciled"))), FinishReason: chat.FinishReasonStop}}
	settlement := compositionValue(interaction.SettleModelResult(model, restored.Request, response))
	if err := recovered.ResolveUnknownEffect(t.Context(), pending.Request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	result := compositionValue(recovered.Await(t.Context()))
	output := compositionValue(result.Output())
	if output.Local != "RECOVER" || output.Model != "reconciled" || calls.Load() != 1 {
		t.Fatalf("output=%+v calls=%d", output, calls.Load())
	}
	view := compositionValue(recovered.Inspect(t.Context()))
	if len(view.Processes) != 1 {
		t.Fatal("completed children retained active state")
	}
}

func TestCanceledCompositionExitsWithItsUnknownDescendantRecorded(t *testing.T) {
	store := agentrun.NewMemoryStore()
	runtime := conformancetest.Runtime(t, agentrun.Config{Store: store})
	var calls atomic.Int32
	program, _ := unknownComposition(t, runtime, &calls)
	handle := compositionValue(runtime.Start(t.Context(), program, agentrun.StartRequest[compositionInput]{ID: agentrun.StartID{Scope: "test", Key: t.Name()}, Input: compositionInput{Prompt: "cancel"}}))
	pending := awaitUnknown(t, handle)
	if err := handle.Cancel(t.Context(), "request withdrawn"); err != nil {
		t.Fatal(err)
	}
	result := compositionValue(handle.Await(t.Context()))
	if result.Termination().Status() != agentrun.StatusCanceled || calls.Load() != 1 {
		t.Fatalf("termination=%+v calls=%d", result.Termination(), calls.Load())
	}
	if _, present := result.Termination().Output(); present {
		t.Fatal("canceled composition published partial output")
	}
	log, _, err := store.Load(t.Context(), handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	var unknownAt, descendantExitAt, rootExitAt agentrun.LSN
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case agentrun.EffectSettled:
			if record.ID == pending.Request.ID() {
				if record.Outcome.Status() != agentrun.SettlementStatusUnknown {
					t.Fatal("cancellation invented a definite model outcome")
				}
				unknownAt = entry.LSN
			}
		case agentrun.Exited:
			switch record.Process {
			case pending.Request.ID().Process:
				descendantExitAt = entry.LSN
			case handle.ID():
				rootExitAt = entry.LSN
			}
		}
	}
	if unknownAt == 0 || descendantExitAt <= unknownAt || rootExitAt <= descendantExitAt {
		t.Fatalf("unknown=%d descendant exit=%d root exit=%d, want the unknown outcome recorded before each Exit", unknownAt, descendantExitAt, rootExitAt)
	}
}
