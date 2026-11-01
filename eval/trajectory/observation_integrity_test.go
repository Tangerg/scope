package trajectory_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/eval/trajectory"
)

// A retrying telemetry transport can redeliver both callbacks although the
// model was called only once. It cannot overwrite the first response or cause
// its token count to be charged twice.
type redeliveringObserver struct{ recorder *trajectory.Recorder }

func (r redeliveringObserver) OnModelStarted(ctx context.Context, inv agent.ModelInvocation, req *chat.Request) {
	r.recorder.OnModelStarted(ctx, inv, req)
	r.recorder.OnModelStarted(ctx, inv, req)
}
func (r redeliveringObserver) OnModelSettled(ctx context.Context, inv agent.ModelInvocation, settled agent.ModelSettlement) {
	r.recorder.OnModelSettled(ctx, inv, settled)
	// The replay claims a conflicting provider response for the same physical attempt.
	replay := settled
	replay.Response = settled.Response.Clone()
	replay.Response.Output.Message = new(chat.NewAssistantMessage(chat.NewTextPart("forged")))
	r.recorder.OnModelSettled(ctx, inv, replay)
}
func TestRedeliveredObservationsNeverReplaceThePhysicalCall(t *testing.T) {
	rec := new(trajectory.Recorder)
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("first"))), FinishReason: chat.FinishReasonStop}, Metadata: &chat.ResponseMetadata{Usage: &chat.Usage{InputTokens: 11, OutputTokens: 3}}}, nil
	})
	process, _, _ := startModelRecording(t, rec, agent.DispatcherConfig{Model: model, Observer: redeliveringObserver{rec}}, run.NewMemoryStore(), false)
	captured, err := rec.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := captured.ModelCalls()
	if len(calls) != 1 || calls[0].Response.Text() != "first" || calls[0].Response.Metadata.Usage.InputTokens != 11 || calls[0].Response.Metadata.Usage.OutputTokens != 3 {
		t.Fatalf("duplicated or replaced physical response: %+v", calls)
	}
	if gaps := captured.Gaps(); gaps.DroppedCallObservations != 2 || gaps.UnpairedCalls != 0 || gaps.DroppedEvents != 0 {
		t.Fatalf("changed evidence loss: %+v", gaps)
	}
}

func TestBackwardFactTimestampDoesNotBecomeAReportedDuration(t *testing.T) {
	source := new(trajectory.Recorder)
	process := runRecordedInteraction(t, source, source, fixtureWeatherTool{})
	captured, err := source.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	observed := new(trajectory.Recorder)
	var start time.Time
	changed := false
	for _, e := range captured.Events() {
		if e.Relation().IsRoot() && e.Name() == run.EventProcessStarted {
			start = e.OccurredAt()
		}
		if !changed && !start.IsZero() && e.Name() == run.EventStepFinished {
			raw, wireErr := jsonv2.Marshal(e)
			if wireErr != nil {
				t.Fatal(wireErr)
			}
			var wire map[string]json.RawMessage
			if wireErr = jsonv2.Unmarshal(raw, &wire); wireErr != nil {
				t.Fatal(wireErr)
			}
			wire["occurred_at"], wireErr = jsonv2.Marshal(start.Add(-time.Second))
			if wireErr != nil {
				t.Fatal(wireErr)
			}
			raw, wireErr = jsonv2.Marshal(wire)
			if wireErr != nil {
				t.Fatal(wireErr)
			}
			if wireErr = jsonv2.Unmarshal(raw, &e); wireErr != nil {
				t.Fatal(wireErr)
			}
			changed = true
		}
		observed.OnEvent(t.Context(), e)
	}
	replay, err := observed.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || replay.Elapsed() != nil || len(replay.Events()) != len(captured.Events()) || replay.Termination().Status() != captured.Termination().Status() {
		t.Fatalf("reordered observation fabricated latency or changed execution: changed=%t elapsed=%v", changed, replay.Elapsed())
	}
}
