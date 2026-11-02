package trajectory_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval/trajectory"
)

func completedResponse(text string, usage *chat.Usage) *chat.Response {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, Metadata: &chat.ResponseMetadata{Usage: usage}}
}

func TestModelFailureRetainsPartialResponseAndAccounting(t *testing.T) {
	response := completedResponse("generated before postprocessing failed", &chat.Usage{InputTokens: 17, OutputTokens: 11})
	cause := &chat.CallFailure{Kind: chat.CallFailureFailed, Cause: errors.New("history commit rejected")}
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return response, cause })
	handle, _, _ := startInteraction(t, model, nil, run.NewMemoryStore())
	result, err := handle.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusFailed {
		t.Fatal(result, err)
	}
	actual := captureInteraction(t, handle)
	calls := actual.ModelCalls()
	if len(calls) != 1 || calls[0].Settlement.Outcome != interaction.ModelOutcomeFailed || calls[0].Settlement.Response.Text() != response.Text() || calls[0].Settlement.Failure == "" {
		t.Fatalf("partial response lost: %+v", calls)
	}
	if tokens, tokenErr := actual.TotalTokens(); tokenErr != nil || tokens != 28 {
		t.Fatal(tokens, tokenErr)
	}
	encoded, err := jsonv2.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var restored trajectory.Trajectory
	if decodeErr := jsonv2.Unmarshal(encoded, &restored); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if tokens, tokenErr := restored.TotalTokens(); tokenErr != nil || tokens != 28 {
		t.Fatal(tokens, tokenErr)
	}
}

func TestModelNonexecutionAndMissingAccountingRemainDistinct(t *testing.T) {
	for _, test := range []struct {
		name      string
		kind      chat.CallFailureKind
		outcome   interaction.ModelOutcome
		zeroKnown bool
	}{
		{"not executed", chat.CallFailureNotExecuted, interaction.ModelOutcomeNotExecuted, true},
		{"failed", chat.CallFailureFailed, interaction.ModelOutcomeFailed, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return nil, &chat.CallFailure{Kind: test.kind, Cause: errors.New("classified model failure")}
			})
			handle, _, _ := startInteraction(t, model, nil, run.NewMemoryStore())
			if _, err := handle.Await(t.Context()); err != nil {
				t.Fatal(err)
			}
			actual := captureInteraction(t, handle)
			if calls := actual.ModelCalls(); len(calls) != 1 || calls[0].Settlement.Outcome != test.outcome {
				t.Fatal(calls)
			}
			tokens, err := actual.TotalTokens()
			if test.zeroKnown {
				if err != nil || tokens != 0 {
					t.Fatal(tokens, err)
				}
			} else if !errors.Is(err, trajectory.ErrIncompleteRecording) {
				t.Fatalf("missing usage became zero: %d %v", tokens, err)
			}
		})
	}
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return completedResponse("done", nil), nil
	})
	handle, _, _ := startInteraction(t, model, nil, run.NewMemoryStore())
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := captureInteraction(t, handle).TotalTokens(); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatalf("success fabricated token accounting: %v", err)
	}
}

func TestTotalTokensRejectsCrossCallOverflow(t *testing.T) {
	client := &fixtureInteractionClient{usage: &chat.Usage{InputTokens: math.MaxInt64}}
	handle, _, _ := startInteraction(t, client, []tool.Tool{fixtureWeatherTool{}}, run.NewMemoryStore())
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := captureInteraction(t, handle).TotalTokens(); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("token overflow accepted: %v", err)
	}
}

func awaitUnknown(t *testing.T, handle *run.Handle[interaction.Output]) run.PendingEffect {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		view, err := handle.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		root, ok := view.Process(handle.ID())
		if !ok {
			t.Fatal("root missing")
		}
		for _, pending := range root.PendingEffects {
			if pending.Outcome.Status() == run.SettlementStatusUnknown {
				return pending
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("unknown effect not committed", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestUnknownResolutionAndRecoveryRetainOneLogicalCallAndWholeHistory(t *testing.T) {
	var dispatched atomic.Uint32
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		dispatched.Add(1)
		return completedResponse("partial response", &chat.Usage{InputTokens: 5}), errors.New("transport acknowledgment lost")
	})
	store := run.NewMemoryStore()
	handle, runtime, program := startInteraction(t, model, nil, store)
	pending := awaitUnknown(t, handle)
	before := captureInteraction(t, handle)
	if before.HistoryComplete() || before.Termination().Valid() {
		t.Fatal("unknown external work invented a root exit")
	}
	if elapsed, known := before.Elapsed(); known || elapsed != 0 {
		t.Fatal("unresolved work invented an end time")
	}
	if calls := before.ModelCalls(); len(calls) != 1 || calls[0].Settlement.Outcome != interaction.ModelOutcomeUnknown || calls[0].Settlement.Response.Text() != "partial response" {
		t.Fatalf("unknown partial response lost: %+v", calls)
	}
	if _, err := before.TotalTokens(); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatal(err)
	}
	oldLog, err := before.Log()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := runtime.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	recoveredRuntime := newRuntime(t, store)
	recovered, err := recoveredRuntime.Recover(t.Context(), program, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	pending = awaitUnknown(t, recovered)
	settlement, err := interaction.SettleModelResult(program, pending.Request, completedResponse("investigated answer", &chat.Usage{InputTokens: 5, OutputTokens: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if resolveErr := recovered.ResolveUnknownEffect(t.Context(), pending.Request.ID(), settlement); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if _, awaitErr := recovered.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	after := captureInteraction(t, recovered)
	if !after.HistoryComplete() || dispatched.Load() != 1 || len(after.ModelCalls()) != 1 {
		t.Fatal("recovery duplicated logical call or lost history", dispatched.Load())
	}
	if coverageErr := after.CheckCoverage(); coverageErr != nil {
		t.Fatal(coverageErr)
	}
	full, err := after.Log()
	if err != nil {
		t.Fatal(err)
	}
	var starts, settlements int
	for _, entry := range full.Entries {
		switch record := entry.Record.(type) {
		case run.Started:
			if record.Process == handle.ID() {
				starts++
			}
		case run.EffectSettled:
			if record.ID == pending.Request.ID() {
				settlements++
			}
		}
	}
	if starts != 1 || settlements != 2 || full.Epoch() <= oldLog.Epoch() {
		t.Fatalf("history starts=%d settlements=%d epoch=%d", starts, settlements, full.Epoch())
	}
	cursor := oldLog.Head()
	for entry, err := range handle.Observe(t.Context(), cursor) {
		if err != nil {
			t.Fatal(err)
		}
		if entry.LSN != cursor+1 {
			t.Fatal("cursor changed across recovery")
		}
		cursor = entry.LSN
	}
	if cursor != full.Head() {
		t.Fatal("old handle could not follow recovered history")
	}
}

func TestModelPanicRetainsInteractionOwnedUnknownEvidence(t *testing.T) {
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { panic("private provider panic") })
	handle, _, _ := startInteraction(t, model, nil, run.NewMemoryStore())
	pending := awaitUnknown(t, handle)
	actual := captureInteraction(t, handle)
	calls := actual.ModelCalls()
	if len(calls) != 1 || calls[0].Settlement.Outcome != interaction.ModelOutcomeUnknown || calls[0].Settlement.Failure == "" {
		t.Fatalf("runtime uncertainty lost: %+v", calls)
	}
	log, err := actual.Log()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range log.Entries {
		if settled, ok := entry.Record.(run.EffectSettled); ok && settled.ID == pending.Request.ID() {
			if settled.Failure != nil {
				t.Fatal("contained model panic acquired an independent runtime failure")
			}
			return
		}
	}
	t.Fatal("unknown record absent")
}

type panickingDispatcher struct{}

func (panickingDispatcher) Dispatch(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
	panic("private dispatcher panic")
}
func (panickingDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func TestRuntimePanicCarriesExplicitUnknownFailure(t *testing.T) {
	runtime := newRuntime(t, run.NewMemoryStore())
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		t.Fatal("replaced dispatcher invoked the model")
		return nil, nil
	})
	original, err := interaction.Compile(runtime.Compiler(), interaction.Config{Name: "test.dispatcher_panic", Description: "Observe one model intent at a failed dispatcher boundary.", Revision: "unreachable-model", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	program, err := run.Bind[interaction.Input, interaction.Output](runtime.Compiler(), run.BindingConfig{Definition: original.Definition(), Dispatcher: panickingDispatcher{}, Revision: "panicking-dispatcher"})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[interaction.Input]{ID: run.StartID{Scope: "trajectory", Key: "dispatcher-panic"}, Input: interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}}})
	if err != nil {
		t.Fatal(err)
	}
	pending := awaitUnknown(t, handle)
	actual := captureInteraction(t, handle)
	calls := actual.ModelCalls()
	if len(calls) != 1 || calls[0].Settlement.Outcome != interaction.ModelOutcomeUnknown || calls[0].Settlement.Failure == "" {
		t.Fatalf("runtime uncertainty lost: %+v", calls)
	}
	log, err := actual.Log()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range log.Entries {
		if settled, ok := entry.Record.(run.EffectSettled); ok && settled.ID == pending.Request.ID() {
			if settled.Failure == nil || string(settled.Outcome.Payload().JSON()) != "null" {
				t.Fatal("runtime failure guessed an interaction envelope")
			}
			return
		}
	}
	t.Fatal("unknown record absent")
}
