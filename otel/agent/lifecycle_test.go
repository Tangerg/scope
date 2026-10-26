package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/agent"
	agentotel "github.com/Tangerg/scope/otel/agent"
)

func TestObserverRetainsTerminationClassificationWithoutFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    agent.Status
		cause     agent.TerminationCause
		errorType string
	}{
		{name: "canceled", status: agent.StatusCanceled, cause: agent.TerminationCauseHostCancellation},
		{name: "deadline", status: agent.StatusTimedOut, cause: agent.TerminationCauseHostDeadline, errorType: "agent.host_deadline"},
		{name: "killed", status: agent.StatusKilled, cause: agent.TerminationCauseEngineKill, errorType: "agent.engine_kill"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				harness := newObserverHarness(t)
				paused := make(chan struct{})
				engine, err := agent.NewEngine(agent.EngineConfig{
					TreeCommitter: agent.NewMemoryTreeCommitter(),
					EventListeners: []agent.EventListener{
						harness.observer,
						agent.EventListenerFunc(func(_ context.Context, event agent.Event) {
							if event.Name() == agent.EventProcessPaused {
								close(paused)
							}
						}),
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
						t.Error(closeErr)
					}
				}()
				ctx, cancel := context.WithCancel(t.Context())
				if test.status == agent.StatusTimedOut {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				}
				defer cancel()
				input, err := agent.EncodePayload(testInput{Value: "pause"})
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(ctx, testDeployment(t), input)
				if err != nil {
					t.Fatal(err)
				}
				<-paused
				switch test.status {
				case agent.StatusCanceled:
					cancel()
				case agent.StatusTimedOut:
					time.Sleep(time.Second)
				case agent.StatusKilled:
					if killErr := process.Kill(t.Context(), "private termination reason"); killErr != nil {
						t.Fatal(killErr)
					}
				}
				result, err := process.Await(t.Context())
				if err != nil || result.Termination().Status() != test.status || result.Termination().Cause() != test.cause {
					t.Fatalf("termination=%+v error=%v", result.Termination(), err)
				}
				if err := process.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
				span := spanByName(t, harness.recorder.Ended(), "invoke_agent test.otel", 0)
				if stringAttribute(span.Attributes(), "error.type") != test.errorType ||
					(span.Status().Code == codes.Error) != (test.errorType != "") {
					t.Fatalf("span status=%v attributes=%v", span.Status(), span.Attributes())
				}
				if test.errorType != "" {
					assertSpanException(t, span, "agent Process "+test.status.String()+": "+test.cause.String())
				}
				if strings.Contains(fmt.Sprint(span.Attributes(), span.Events(), span.Status()), "private termination reason") {
					t.Fatal("caller termination diagnostics entered telemetry")
				}
				var metrics metricdata.ResourceMetrics
				if err := harness.reader.Collect(t.Context(), &metrics); err != nil {
					t.Fatal(err)
				}
				assertSumAttribute(t, metricByName(t, metrics, "agent.process.exits"), "agent.process.cause", test.cause.String())
				duration := metricByName(t, metrics, "gen_ai.invoke_agent.duration")
				if histogramCount(t, duration) != 1 {
					t.Fatal("termination did not record exactly one activation duration")
				}
				if test.errorType != "" {
					assertHistogramAttribute(t, duration, "error.type", test.errorType)
				}
			})
		})
	}
}

func TestObserverCloseEndsEveryIncompleteSpanOnce(t *testing.T) {
	harness := newObserverHarness(t)
	for _, event := range captureObserverEvents(t) {
		step, _ := event.StepSequence()
		if event.Name() == agent.EventProcessStarted ||
			(step == 1 && (event.Name() == agent.EventStepStarted || event.Name() == agent.EventEffectStarted)) {
			harness.observer.OnEvent(t.Context(), event)
		}
	}
	harness.observer.Close()
	harness.observer.Close()
	spans := harness.recorder.Ended()
	if len(spans) != 3 {
		t.Fatalf("ended spans=%d, want one process, step, and effect", len(spans))
	}
	for _, span := range spans {
		assertSpanException(t, span, "agent otel: observer closed before span completion")
	}
	var metrics metricdata.ResourceMetrics
	if err := harness.reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == "agent.process.exits" {
				t.Fatal("closing observation invented a logical process exit")
			}
		}
	}
}

func TestObserverRejectsNilContextWithoutRetainingInFlightWork(t *testing.T) {
	harness := newObserverHarness(t)
	event := captureProcessStartedEvent(t)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		harness.observer.OnEvent(nil, event) //nolint:staticcheck // This invalid context proves admission cannot retain in-flight work.
	}()
	if recovered == nil {
		t.Fatal("nil context was accepted")
	}
	harness.observer.Close()
	if len(harness.recorder.Started()) != 0 {
		t.Fatal("invalid context started observation")
	}
}

func TestObserverIgnoresMissingReceiverAndInvalidFacts(t *testing.T) {
	var missing *agentotel.Observer
	missing.OnEvent(t.Context(), agent.Event{})
	missing.Close()
	harness := newObserverHarness(t)
	harness.observer.OnEvent(t.Context(), agent.Event{})
	if len(harness.recorder.Started()) != 0 {
		t.Fatal("invalid fact started observation")
	}
}

func TestWrapDispatcherRejectsInvalidConstruction(t *testing.T) {
	for _, observer := range []*agentotel.Observer{nil, {}} {
		if dispatcher, err := observer.WrapDispatcher(testDispatcher{}); dispatcher != nil || !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
			t.Fatalf("dispatcher=%v error=%v", dispatcher, err)
		}
	}
	harness := newObserverHarness(t)
	var typedNil *testDispatcher
	for _, next := range []agent.Dispatcher{nil, typedNil} {
		if dispatcher, err := harness.observer.WrapDispatcher(next); dispatcher != nil || !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
			t.Fatalf("dispatcher=%v error=%v", dispatcher, err)
		}
	}
}

func TestClosedObserverPreservesCommitterInvocation(t *testing.T) {
	harness := newObserverHarness(t)
	cause := errors.New("storage unavailable")
	next := &failingObservedDurability{err: cause}
	committer, err := harness.observer.WrapTreeCommitter(next)
	if err != nil {
		t.Fatal(err)
	}
	harness.observer.Close()
	if err := committer.CommitCheckpoint(t.Context(), agent.TreeCheckpoint{}); err != cause || next.calls != 1 { //nolint:errorlint // A closed observer must return the identical error without wrapping it.
		t.Fatalf("error=%v calls=%d", err, next.calls)
	}
	if len(harness.recorder.Started()) != 0 {
		t.Fatal("closed observer started a committer span")
	}
}
