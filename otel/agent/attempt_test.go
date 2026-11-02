package agent_test

import (
	"context"
	startrand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/agent/run"
)

func TestObserverSeparatesAttemptsFromUnknownResolution(t *testing.T) {
	for _, failureMode := range []string{"unknown", "error", "panic"} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay_%t", failureMode, replay), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					harness := newObserverHarness(t)
					var eventMu sync.Mutex
					var events []run.Event
					finished := make(chan run.Event, 2)
					listener := run.EventListenerFunc(func(_ context.Context, event run.Event) {
						eventMu.Lock()
						events = append(events, event)
						eventMu.Unlock()
						if event.Name() == run.EventEffectFinished {
							finished <- event
						}
					})
					engine, err := run.New(t.Context(), run.Config{
						Store:          run.NewMemoryStore(),
						EventListeners: []run.EventListener{harness.observer, listener},
						DeltaListeners: []run.DeltaListener{run.DeltaListenerFunc(func(context.Context, run.Delta) {})},
					})
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
							t.Error(closeErr)
						}
					}()
					dispatcher := &uncertainDispatcher{mode: failureMode}
					input, err := run.EncodePayload(testInput{Value: "resolution"})
					if err != nil {
						t.Fatal(err)
					}
					process, err := engine.Start(t.Context(), testDeploymentWithDispatcher(t, dispatcher), run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
					if err != nil {
						t.Fatal(err)
					}
					first := <-finished
					effectID, _ := first.EffectID()
					firstFinished, _ := first.EffectFinished()
					synctest.Wait()
					time.Sleep(time.Hour)
					if replay {
						err = process.ReplayUnknownEffect(t.Context(), effectID)
					} else {
						settlement, settleErr := run.NewSettlement(run.SettlementStatusSucceeded, json.RawMessage(`{"ok":true}`))
						if settleErr != nil {
							t.Fatal(settleErr)
						}
						err = process.ResolveUnknownEffect(t.Context(), effectID, settlement)
					}
					if err != nil {
						t.Fatal(err)
					}
					if _, err := process.Await(t.Context()); err != nil {
						t.Fatal(err)
					}
					if err := process.Join(t.Context()); err != nil {
						t.Fatal(err)
					}
					if err := engine.FlushObservations(t.Context()); err != nil {
						t.Fatal(err)
					}
					wantAttempts := 1
					if replay {
						wantAttempts++
					}
					effects := spansByName(harness.recorder.Ended(), "agent.effect")
					if len(effects) != wantAttempts {
						t.Fatalf("effect spans = %d, want %d physical attempts", len(effects), wantAttempts)
					}
					if got := effects[0].EndTime().Sub(effects[0].StartTime()); got != 3*time.Millisecond {
						t.Fatalf("uncertain attempt duration = %s, want 3ms without adjudication delay", got)
					}
					if got := stringAttribute(effects[0].Attributes(), "agent.effect.attempt_id"); got != firstFinished.AttemptID().String() {
						t.Fatalf("attempt identity = %q, want %q", got, firstFinished.AttemptID().String())
					}
					wantKind, wantCode := "", ""
					switch failureMode {
					case "error":
						wantKind, wantCode = "external", "engine.dispatch.failed"
					case "panic":
						wantKind, wantCode = "panic", "engine.dispatch.panicked"
					}
					if stringAttribute(effects[0].Attributes(), "agent.failure.kind") != wantKind || stringAttribute(effects[0].Attributes(), "agent.failure.code") != wantCode {
						t.Fatalf("attempt failure classification = %v", effects[0].Attributes())
					}
					if replay {
						second := <-finished
						secondFinished, _ := second.EffectFinished()
						if got := stringAttribute(effects[1].Attributes(), "agent.effect.attempt_id"); got != secondFinished.AttemptID().String() || got == firstFinished.AttemptID().String() {
							t.Fatal("replayed physical attempt lost its Engine identity")
						}
						if effects[1].EndTime().Sub(effects[1].StartTime()) != 7*time.Millisecond {
							t.Fatal("replay span includes time outside its invocation")
						}
					}
					processSpan := spanByName(t, harness.recorder.Ended(), "invoke_agent test.otel", 0)
					resolved, dropped := 0, 0
					for _, event := range processSpan.Events() {
						if event.Name != run.EventEffectResolved && event.Name != run.EventDeltaDropped {
							continue
						}
						if stringAttribute(event.Attributes, "agent.effect.id") != effectID.String() {
							t.Fatal("process event lost the logical Effect association")
						}
						if event.Name == run.EventEffectResolved {
							resolved++
							if stringAttribute(event.Attributes, "agent.effect.target") != "dispatcher" || stringAttribute(event.Attributes, "agent.effect.status") != "succeeded" || stringAttribute(event.Attributes, "agent.effect.attempt_id") != "" {
								t.Fatal("adjudication must expose the definite outcome without inventing an attempt")
							}
						} else {
							dropped++
							if stringAttribute(event.Attributes, "agent.effect.attempt_id") != stringAttribute(effects[dropped-1].Attributes(), "agent.effect.attempt_id") {
								t.Fatal("dropped Delta is not associated with its physical attempt")
							}
						}
					}
					if resolved != 1 || dropped != wantAttempts {
						t.Fatalf("resolution/drop events = %d/%d", resolved, dropped)
					}
					var metrics metricdata.ResourceMetrics
					if err := harness.reader.Collect(t.Context(), &metrics); err != nil {
						t.Fatal(err)
					}
					duration := metricByName(t, metrics, "agent.effect.duration")
					if histogramCount(t, duration) != uint64(wantAttempts) {
						t.Fatal("resolution added a duration measurement")
					}
					for _, point := range duration.Data.(metricdata.Histogram[float64]).DataPoints {
						for _, key := range []attribute.Key{"agent.effect.attempt_id", "agent.effect.id", "agent.process.id", "agent.tree.incarnation_id"} {
							if point.Attributes.HasValue(key) {
								t.Fatalf("high-cardinality identity %s entered metrics", key)
							}
						}
					}
					for _, span := range harness.recorder.Ended() {
						if strings.Contains(fmt.Sprint(span.Attributes(), span.Events(), span.Status()), "private-dispatch-payload") {
							t.Fatal("raw Dispatcher failure entered telemetry")
						}
					}
					eventMu.Lock()
					defer eventMu.Unlock()
					if replay && failureMode == "unknown" {
						checkReplayAfterLostFinish(t, events, dispatcher.attempts)
					}
					for _, event := range events {
						if fact, ok := event.EffectStarted(); ok {
							if fact.AttemptID() != dispatcher.attempts[0] && (len(dispatcher.attempts) != 2 || fact.AttemptID() != dispatcher.attempts[1]) {
								t.Fatal("event and Dispatch disagree about the Engine attempt")
							}
						}
					}
				})
			})
		}
	}
}

func checkReplayAfterLostFinish(t *testing.T, events []run.Event, attempts []run.EffectAttemptID) {
	t.Helper()
	harness := newObserverHarness(t)
	var replayStart, replayFinish, processFinish time.Time
	for _, event := range events {
		if fact, started := event.EffectStarted(); started && fact.AttemptID() == attempts[1] {
			replayStart = event.OccurredAt()
		}
		if fact, finished := event.EffectFinished(); finished {
			if fact.AttemptID() == attempts[0] {
				continue
			}
			replayFinish = event.OccurredAt()
		}
		if event.Name() == run.EventProcessFinished {
			processFinish = event.OccurredAt()
		}
		harness.observer.OnEvent(t.Context(), event)
	}
	effects := spansByName(harness.recorder.Ended(), "agent.effect")
	if len(effects) != 2 {
		t.Fatalf("replay after a dropped finish ended %d physical spans, want 2", len(effects))
	}
	replay, incomplete := effects[0], effects[1]
	if id := stringAttribute(replay.Attributes(), "agent.effect.attempt_id"); id != attempts[1].String() || !replay.StartTime().Equal(replayStart) || !replay.EndTime().Equal(replayFinish) {
		t.Fatalf("replay acquired another attempt's span: id=%q start=%v finish=%v", id, replay.StartTime(), replay.EndTime())
	}
	if id := stringAttribute(incomplete.Attributes(), "agent.effect.attempt_id"); id != attempts[0].String() || !incomplete.EndTime().Equal(processFinish) || stringAttribute(incomplete.Attributes(), "agent.effect.status") != "" {
		t.Fatalf("missing finish acquired a settlement: id=%q end=%v attributes=%v", id, incomplete.EndTime(), incomplete.Attributes())
	}
	assertSpanException(t, incomplete, "agent otel: span completion was not observed")
}

type uncertainDispatcher struct {
	testDispatcher
	mode     string
	attempts []run.EffectAttemptID
}

func (u *uncertainDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	attemptID, _ := request.AttemptID()
	u.attempts = append(u.attempts, attemptID)
	emit(json.RawMessage("private-dispatch-payload"))
	if len(u.attempts) > 1 {
		time.Sleep(7 * time.Millisecond)
		return u.testDispatcher.Dispatch(ctx, request, emit)
	}
	time.Sleep(3 * time.Millisecond)
	switch u.mode {
	case "error":
		return run.Settlement{}, errors.New("private-dispatch-payload")
	case "panic":
		panic("private-dispatch-payload")
	default:
		return run.NewSettlement(run.SettlementStatusUnknown, json.RawMessage(`null`))
	}
}
