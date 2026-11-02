package agent_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/agent/run"
	agentotel "github.com/Tangerg/scope/otel/agent"
)

func TestObserverRetainsTerminationClassificationWithoutFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    run.Status
		cause     run.TerminationCause
		errorType string
	}{
		{name: "canceled", status: run.StatusCanceled, cause: run.TerminationCauseHostCancellation},
		{name: "deadline", status: run.StatusTimedOut, cause: run.TerminationCauseHostDeadline, errorType: "agent.host_deadline"},
		{name: "killed", status: run.StatusKilled, cause: run.TerminationCauseEngineKill, errorType: "agent.engine_kill"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				harness := newObserverHarness(t)
				paused := make(chan run.ProcessID, 1)
				engine, err := run.New(t.Context(), run.Config{
					Store: run.NewMemoryStore(),
					EventListeners: []run.EventListener{
						harness.observer,
						run.EventListenerFunc(func(_ context.Context, event run.Event) {
							if event.Name() == run.EventProcessPaused {
								paused <- event.Relation().ProcessID()
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
				if test.status == run.StatusTimedOut {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				}
				defer cancel()
				input, err := run.EncodePayload(testInput{Value: "pause"})
				if err != nil {
					t.Fatal(err)
				}
				var process *run.Handle[run.Payload]
				var foreground chan error
				program := testDeployment(t)
				if test.status == run.StatusTimedOut {
					foreground = make(chan error, 1)
					go func() { _, runErr := engine.Run(ctx, program, input); foreground <- runErr }()
					id := <-paused
					var found bool
					process, found = engine.Process(id)
					if !found {
						t.Fatal("foreground execution missing before deadline")
					}
				} else {
					process, err = engine.Start(ctx, program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
					if err != nil {
						t.Fatal(err)
					}
					<-paused
				}
				switch test.status {
				case run.StatusCanceled:
					if cancelErr := process.Cancel(t.Context(), "private termination reason"); cancelErr != nil {
						t.Fatal(cancelErr)
					}
				case run.StatusTimedOut:
					time.Sleep(time.Second)
				case run.StatusKilled:
					if killErr := process.Kill(t.Context(), "private termination reason"); killErr != nil {
						t.Fatal(killErr)
					}
				}
				if foreground != nil {
					if ended, ok := errors.AsType[*run.TerminationError](<-foreground); !ok || ended.Termination.Status() != run.StatusTimedOut {
						t.Fatal("foreground timeout missing")
					}
				}
				result, err := process.Await(t.Context())
				if err != nil || result.Termination().Status() != test.status || result.Termination().Cause() != test.cause {
					t.Fatalf("termination=%+v error=%v", result.Termination(), err)
				}
				if err := process.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
				if flushErr := engine.FlushObservations(t.Context()); flushErr != nil {
					t.Fatal(flushErr)
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
		if event.Name() == run.EventProcessStarted ||
			(step == 1 && (event.Name() == run.EventStepStarted || event.Name() == run.EventEffectStarted)) {
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
		assertSpanException(t, span, "agent otel: span completion was not observed")
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
	missing.OnEvent(t.Context(), run.Event{})
	missing.Close()
	harness := newObserverHarness(t)
	harness.observer.OnEvent(t.Context(), run.Event{})
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
	for _, next := range []run.Dispatcher{nil, typedNil} {
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
	if err := committer.CommitCheckpoint(t.Context(), run.TreeCheckpoint{}); err != cause || next.calls != 1 { //nolint:errorlint // A closed observer must return the identical error without wrapping it.
		t.Fatalf("error=%v calls=%d", err, next.calls)
	}
	if len(harness.recorder.Started()) != 0 {
		t.Fatal("closed observer started a committer span")
	}
}
