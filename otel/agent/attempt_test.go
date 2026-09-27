package agent_test

import (
	"context"
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

	"github.com/Tangerg/scope/agent"
)

func TestObserverSeparatesAttemptsFromUnknownResolution(t *testing.T) {
	for _, failureMode := range []string{"unknown", "error", "panic"} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay_%t", failureMode, replay), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					harness := newObserverHarness(t)
					var eventMu sync.Mutex
					var events []agent.Event
					finished := make(chan agent.Event, 2)
					listener := agent.EventListenerFunc(func(_ context.Context, event agent.Event) {
						eventMu.Lock()
						events = append(events, event)
						eventMu.Unlock()
						if event.Name() == agent.EventEffectFinished {
							finished <- event
						}
					})
					engine, err := agent.NewEngine(agent.EngineConfig{
						TreeCommitter:  agent.NewMemoryTreeCommitter(),
						EventListeners: []agent.EventListener{harness.observer, listener},
						DeltaListeners: []agent.DeltaListener{agent.DeltaListenerFunc(func(context.Context, agent.Delta) {})},
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
					input, err := agent.EncodePayload(testInput{Value: "resolution"})
					if err != nil {
						t.Fatal(err)
					}
					process, err := engine.Start(t.Context(), testDeploymentWithDispatcher(t, dispatcher), input)
					if err != nil {
						t.Fatal(err)
					}
					first := <-finished
					effectID, _ := first.EffectID()
					firstFact, _ := first.EffectFinished()
					synctest.Wait()
					time.Sleep(time.Hour)
					if replay {
						err = process.ReplayUnknownEffect(t.Context(), effectID)
					} else {
						settlement, settleErr := agent.NewSettlement(effectID, agent.SettlementStatusSucceeded, json.RawMessage(`{"ok":true}`))
						if settleErr != nil {
							t.Fatal(settleErr)
						}
						err = process.ResolveUnknownEffect(t.Context(), settlement)
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
					if got := stringAttribute(effects[0].Attributes(), "agent.effect.attempt_id"); got != firstFact.AttemptID().String() {
						t.Fatalf("attempt identity = %q, want %q", got, firstFact.AttemptID().String())
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
						secondFact, _ := second.EffectFinished()
						if got := stringAttribute(effects[1].Attributes(), "agent.effect.attempt_id"); got != secondFact.AttemptID().String() || got == firstFact.AttemptID().String() {
							t.Fatal("replayed physical attempt lost its Engine identity")
						}
						if effects[1].EndTime().Sub(effects[1].StartTime()) != 7*time.Millisecond {
							t.Fatal("replay span includes time outside its invocation")
						}
					}
					processSpan := spanByName(t, harness.recorder.Ended(), "invoke_agent test.otel", 0)
					resolved, dropped := 0, 0
					for _, event := range processSpan.Events() {
						if event.Name != agent.EventEffectResolved && event.Name != agent.EventDeltaDropped {
							continue
						}
						if stringAttribute(event.Attributes, "agent.effect.id") != effectID.String() {
							t.Fatal("process event lost the logical Effect association")
						}
						if event.Name == agent.EventEffectResolved {
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

type uncertainDispatcher struct {
	testDispatcher
	mode     string
	attempts []agent.EffectAttemptID
}

func (u *uncertainDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
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
		return agent.Settlement{}, errors.New("private-dispatch-payload")
	case "panic":
		panic("private-dispatch-payload")
	default:
		return agent.NewSettlement(request.ID(), agent.SettlementStatusUnknown, json.RawMessage(`null`))
	}
}
