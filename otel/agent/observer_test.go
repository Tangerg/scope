package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	agentotel "github.com/Tangerg/scope/otel/agent"
)

func TestObserverTracesRealProcessStepAndEffectLifecycle(t *testing.T) {
	harness := newObserverHarness(t)
	result := runObservedProcess(t, harness.observer)
	assertObservedSpans(t, harness.recorder.Ended(), result)
	assertObservedMetrics(t, harness.reader, result)
	process := spanByName(t, harness.recorder.Ended(), "invoke_agent test.otel", 0)
	if stringAttribute(process.Attributes(), "gen_ai.operation.name") != "invoke_agent" || stringAttribute(process.Attributes(), "gen_ai.agent.name") != "test.otel" {
		t.Error("agent span is missing its GenAI identity")
	}
	if process.SpanKind().String() != "internal" || stringAttribute(process.Attributes(), "gen_ai.agent.id") != "" {
		t.Error("local invocation has incorrect kind or a fabricated stable agent ID")
	}
	var metrics metricdata.ResourceMetrics
	if err := harness.reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	duration := metricByName(t, metrics, "gen_ai.invoke_agent.duration")
	point := duration.Data.(metricdata.Histogram[float64]).DataPoints[0]
	if point.Sum != process.EndTime().Sub(process.StartTime()).Seconds() {
		t.Error("agent metric duration differs from its span")
	}
	if !slices.Equal(point.Bounds, []float64{0.1, 0.2, 0.4, 0.8, 1.6, 3.2, 6.4, 12.8, 25.6, 51.2, 102.4, 204.8, 409.6}) {
		t.Errorf("agent duration bounds = %v", point.Bounds)
	}
	assertHistogramAttribute(t, duration, "gen_ai.agent.name", "test.otel")
}

type observerHarness struct {
	provider *sdktrace.TracerProvider
	recorder *tracetest.SpanRecorder
	reader   *sdkmetric.ManualReader
	observer *agentotel.Observer
}

func newObserverHarness(t *testing.T) observerHarness {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{
		TracerProvider: provider, MeterProvider: meterProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	return observerHarness{provider: provider, recorder: recorder, reader: reader, observer: observer}
}

func runObservedProcess(t *testing.T, observer *agentotel.Observer) agent.Result {
	t.Helper()
	deployment := testDeployment(t)
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeDurability: agenttest.NewMemoryTreeDurability(),
		EventListeners: []agent.EventListener{observer},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(testInput{Value: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result status = %s, error = %v", result.Status(), err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertObservedSpans(t *testing.T, spans []sdktrace.ReadOnlySpan, result agent.Result) {
	t.Helper()
	if len(spans) != 4 {
		for _, span := range spans {
			t.Logf("span %s parent=%s", span.Name(), span.Parent().SpanID())
		}
		t.Fatalf("ended spans = %d, want process + two steps + effect", len(spans))
	}
	process := spanByName(t, spans, "invoke_agent test.otel", 0)
	steps := spansByName(spans, "agent.step")
	effect := spanByName(t, spans, "agent.effect", 0)
	if len(steps) != 2 {
		t.Fatalf("step spans = %d, want 2", len(steps))
	}
	for _, span := range append(steps, effect) {
		if span.Parent().SpanID() != process.SpanContext().SpanID() {
			t.Fatalf("span %s parent = %s, want process %s", span.Name(), span.Parent().SpanID(), process.SpanContext().SpanID())
		}
		if span.EndTime().Before(span.StartTime()) {
			t.Fatalf("span %s has negative duration", span.Name())
		}
	}
	assertObservedSpanIdentity(t, process, effect)
	incarnationID := stringAttribute(process.Attributes(), "agent.tree.incarnation_id")
	if incarnationID == "" {
		t.Fatal("durable Process span is missing tree incarnation attribution")
	}
	for _, span := range append(steps, effect) {
		if got := stringAttribute(span.Attributes(), "agent.process.id"); got != result.ProcessID().String() {
			t.Fatalf("span %s Process ID = %q", span.Name(), got)
		}
		if got := stringAttribute(span.Attributes(), "agent.tree.incarnation_id"); got != incarnationID {
			t.Fatalf("span %s incarnation = %q, want %q", span.Name(), got, incarnationID)
		}
	}
}

func assertObservedSpanIdentity(t *testing.T, process, effect sdktrace.ReadOnlySpan) {
	t.Helper()
	if got := stringAttribute(process.Attributes(), "agent.deployment.name"); got != "test.otel" {
		t.Fatalf("deployment name attribute = %q", got)
	}
	if got := stringAttribute(process.Attributes(), "agent.process.activation"); got != "started" {
		t.Fatalf("process activation attribute = %q", got)
	}
	if got := stringAttribute(effect.Attributes(), "agent.effect.target"); got != "dispatcher" {
		t.Fatalf("effect target attribute = %q", got)
	}
	if got := stringAttribute(effect.Attributes(), "agent.effect.status"); got != "succeeded" {
		t.Fatalf("effect status attribute = %q", got)
	}
}

func assertObservedMetrics(t *testing.T, reader *sdkmetric.ManualReader, result agent.Result) {
	t.Helper()
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	if got := int64Sum(t, metricByName(t, metrics, "agent.process.activations")); got != 1 {
		t.Fatalf("process activations = %d, want 1", got)
	}
	if got := int64Sum(t, metricByName(t, metrics, "agent.process.exits")); got != 1 {
		t.Fatalf("process exits = %d, want 1", got)
	}
	if got := histogramCount(t, metricByName(t, metrics, "agent.step.duration")); got != 2 {
		t.Fatalf("step duration observations = %d, want 2", got)
	}
	if got := histogramCount(t, metricByName(t, metrics, "agent.effect.duration")); got != 1 {
		t.Fatalf("effect duration observations = %d, want 1", got)
	}
	if got := histogramCount(t, metricByName(t, metrics, "gen_ai.invoke_agent.duration")); got != 1 {
		t.Fatalf("process duration observations = %d, want 1", got)
	}
	for _, name := range []string{
		"gen_ai.invoke_agent.duration", "agent.step.duration", "agent.effect.duration",
	} {
		if got := metricByName(t, metrics, name).Unit; got != "s" {
			t.Fatalf("metric %q unit = %q, want seconds", name, got)
		}
	}
	usage := result.Usage()
	assertInt64HistogramSum(t, metricByName(t, metrics, "agent.process.committed_steps"), int64(usage.CommittedSteps))
	assertInt64HistogramSum(t, metricByName(t, metrics, "agent.process.prepared_effects"), int64(usage.PreparedEffects))
	assertInt64HistogramSum(t, metricByName(t, metrics, "agent.process.accepted_signals"), int64(usage.AcceptedSignals))
	assertHistogramAttribute(t, metricByName(t, metrics, "agent.step.duration"), "agent.deployment.name", "test.otel")
	assertHistogramAttribute(t, metricByName(t, metrics, "agent.effect.duration"), "agent.deployment.name", "test.otel")
	assertSumAttribute(t, metricByName(t, metrics, "agent.process.activations"), "agent.process.activation", "started")
}

func TestObserverRecordsStableProcessFailureAttribution(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{
		TracerProvider: provider, MeterProvider: meterProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	engine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{observer}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(testInput{Value: testValueProcessFailure})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), testDeployment(t), input)
	if err != nil || result.Status() != agent.StatusFailed {
		t.Fatalf("result = %s, error = %v", result.Status(), err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	process := spanByName(t, recorder.Ended(), "invoke_agent test.otel", 0)
	if got := stringAttribute(process.Attributes(), "agent.failure.kind"); got != "execution" {
		t.Fatalf("failure kind = %q", got)
	}
	if got := stringAttribute(process.Attributes(), "agent.failure.code"); got != "test.otel.failed" {
		t.Fatalf("failure code = %q", got)
	}
	assertSpanException(
		t, process,
		"agent Process failed: execution/test.otel.failed",
	)
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	if got := stringAttribute(process.Attributes(), "error.type"); got != "test.otel.failed" {
		t.Errorf("process error.type = %q", got)
	}
	assertHistogramAttribute(t, metricByName(t, metrics, "gen_ai.invoke_agent.duration"), "error.type", "test.otel.failed")
	exits := metricByName(t, metrics, "agent.process.exits")
	assertSumAttribute(t, exits, "agent.failure.kind", "execution")
	assertSumAttribute(t, exits, "agent.failure.code", "test.otel.failed")
}

func TestObserverRecordsStepAndEffectFactErrors(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		spanName    string
		wantStatus  agent.Status
		wantMessage string
	}{
		{
			name: "Step failure", value: testValueStepFailure,
			spanName: "agent.step", wantStatus: agent.StatusFailed,
			wantMessage: "agent Execution Step failed",
		},
		{
			name: "Effect failure", value: testValueEffectFailure,
			spanName: "agent.effect", wantStatus: agent.StatusCompleted,
			wantMessage: "agent dispatcher Effect failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			observer, err := agentotel.NewObserver(agentotel.ObserverConfig{TracerProvider: provider})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(observer.Close)
			engine, err := agent.NewEngine(agent.EngineConfig{
				EventListeners: []agent.EventListener{observer},
			})
			if err != nil {
				t.Fatal(err)
			}
			input, err := agent.EncodeInput(testInput{Value: test.value})
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Run(t.Context(), testDeployment(t), input)
			if err != nil || result.Status() != test.wantStatus {
				t.Fatalf("result = %s, error = %v", result.Status(), err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}

			span := spanByName(t, recorder.Ended(), test.spanName, 0)
			assertSpanException(t, span, test.wantMessage)
		})
	}
}

func TestObserverDistinguishesRestoredProcessActivation(t *testing.T) {
	deployment := testDeployment(t)
	paused := make(chan struct{}, 1)
	source, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{
		agent.EventListenerFunc(func(_ context.Context, event agent.Event) {
			if event.Name() == agent.EventProcessPaused {
				paused <- struct{}{}
			}
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(testInput{Value: "pause"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := source.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	<-paused
	snapshot, err := source.CaptureTree(context.Background(), original.ID())
	if err != nil {
		t.Fatal(err)
	}
	if killErr := original.Kill(context.Background(), "source cleanup"); killErr != nil {
		t.Fatal(killErr)
	}
	if _, awaitErr := original.Await(context.Background()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	if closeErr := source.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{
		TracerProvider: provider, MeterProvider: meterProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	restoredEngine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{observer}})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result, awaitErr := restored.Await(context.Background()); awaitErr != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("restored result = %s termination=%+v, error = %v", result.Status(), result.Termination(), awaitErr)
	}
	if err := restoredEngine.Close(); err != nil {
		t.Fatal(err)
	}

	process := spanByName(t, recorder.Ended(), "invoke_agent test.otel", 0)
	if got := stringAttribute(process.Attributes(), "agent.process.activation"); got != "restored" {
		t.Fatalf("restored Process activation = %q", got)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	assertSumAttribute(t, metricByName(t, metrics, "agent.process.activations"), "agent.process.activation", "restored")
	assertHistogramAttribute(
		t, metricByName(t, metrics, "gen_ai.invoke_agent.duration"),
		"agent.process.activation", "restored",
	)
}

func TestObserverRejectsTypedNilTracerProvider(t *testing.T) {
	var provider *sdktrace.TracerProvider
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{TracerProvider: provider})
	if observer != nil || !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatalf("observer = %v, error = %v", observer, err)
	}
}

func TestObserverIgnoresEventsAfterClose(t *testing.T) {
	events := captureObserverEvents(t)
	metrics := collectClosedObserverMetrics(t, events)
	assertNoRecordedMetrics(t, metrics)
}

func captureObserverEvents(t *testing.T) []agent.Event {
	t.Helper()
	var events []agent.Event
	engine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{
		agent.EventListenerFunc(func(_ context.Context, event agent.Event) {
			events = append(events, event)
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(testInput{Value: "closed observer"})
	if err != nil {
		t.Fatal(err)
	}
	if result, runErr := engine.Run(context.Background(), testDeployment(t), input); runErr != nil || !result.Valid() {
		t.Fatalf("result = %#v, error = %v", result, runErr)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return events
}

func collectClosedObserverMetrics(t *testing.T, events []agent.Event) metricdata.ResourceMetrics {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{MeterProvider: meterProvider})
	if err != nil {
		t.Fatal(err)
	}
	observer.Close()
	for _, event := range events {
		observer.OnEvent(context.Background(), event)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	return metrics
}

func assertNoRecordedMetrics(t *testing.T, metrics metricdata.ResourceMetrics) {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			switch data := metric.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range data.DataPoints {
					if point.Value != 0 {
						t.Fatalf("metric %q recorded %d after Close", metric.Name, point.Value)
					}
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					if point.Count != 0 {
						t.Fatalf("metric %q recorded %d samples after Close", metric.Name, point.Count)
					}
				}
			}
		}
	}
}

func TestObserverCloseRecordsIncompleteSpanError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{TracerProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	observer.OnEvent(t.Context(), captureProcessStartedEvent(t))
	observer.Close()

	process := spanByName(t, recorder.Ended(), "invoke_agent test.otel", 0)
	assertSpanException(t, process, "agent otel: observer closed before span completion")
}

func TestObserverCloseWaitsForInFlightObservation(t *testing.T) {
	event := captureProcessStartedEvent(t)
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		provider := startsBlockingMeterProvider{
			MeterProvider: noop.NewMeterProvider(), entered: entered, release: release,
		}
		observer, err := agentotel.NewObserver(agentotel.ObserverConfig{MeterProvider: provider})
		if err != nil {
			t.Fatal(err)
		}
		observed := make(chan struct{})
		go func() {
			observer.OnEvent(context.Background(), event)
			close(observed)
		}()
		<-entered

		const concurrentCloseCalls = 2
		closed := make([]chan struct{}, concurrentCloseCalls)
		for index := range closed {
			closed[index] = make(chan struct{})
			go func(done chan struct{}) {
				observer.Close()
				close(done)
			}(closed[index])
		}
		synctest.Wait()
		for _, done := range closed {
			select {
			case <-done:
				t.Fatal("Close returned while an observation was still in flight")
			default:
			}
		}

		close(release)
		synctest.Wait()
		select {
		case <-observed:
		default:
			t.Fatal("observation did not finish after its metric was released")
		}
		for _, done := range closed {
			select {
			case <-done:
			default:
				t.Fatal("Close did not finish after the in-flight observation")
			}
		}
	})
}

type startsBlockingMeterProvider struct {
	metric.MeterProvider
	entered chan struct{}
	release <-chan struct{}
}

func (s startsBlockingMeterProvider) Meter(name string, options ...metric.MeterOption) metric.Meter {
	return startsBlockingMeter{
		Meter: s.MeterProvider.Meter(name, options...), entered: s.entered, release: s.release,
	}
}

type startsBlockingMeter struct {
	metric.Meter
	entered chan struct{}
	release <-chan struct{}
}

func (s startsBlockingMeter) Int64Counter(
	name string,
	options ...metric.Int64CounterOption,
) (metric.Int64Counter, error) {
	counter, err := s.Meter.Int64Counter(name, options...)
	if err != nil || name != "agent.process.activations" {
		return counter, err
	}
	return &blockingCounter{
		Int64Counter: counter, entered: s.entered, release: s.release,
	}, nil
}

type blockingCounter struct {
	metric.Int64Counter
	once    sync.Once
	entered chan struct{}
	release <-chan struct{}
}

func (b *blockingCounter) Add(ctx context.Context, value int64, options ...metric.AddOption) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	b.Int64Counter.Add(ctx, value, options...)
}

type testInput struct {
	Value string `json:"value"`
}

type testOutput struct {
	Value string `json:"value"`
}

const (
	testValueProcessFailure                     = "process_failure"
	testValueStepFailure                        = "step_failure"
	testValueEffectFailure                      = "effect_failure"
	testEffectObserve       testEffectOperation = "observe"
	testEffectFail          testEffectOperation = "fail"
)

type testEffectOperation string

type testEffectRequest struct {
	Operation testEffectOperation `json:"operation"`
}

type testEffectResponse struct {
	OK bool `json:"ok"`
}

type testDefinition struct {
	descriptor agent.Descriptor
}

func (t testDefinition) Descriptor() agent.Descriptor { return t.descriptor }

func (testDefinition) Start(input agent.Input) (agent.Execution, error) {
	value, err := input.Decode[testInput]()
	if err != nil {
		return nil, err
	}
	return &testExecution{Value: value.Value}, nil
}

func (testDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != "test.otel" {
		return nil, agent.ErrInvalidExecutionState
	}
	var execution testExecution
	if err := json.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

type testExecution struct {
	Value string `json:"value"`
	Phase uint8  `json:"phase"`
}

func (t *testExecution) Step(context.Context, []agent.Signal) (agent.Transition, error) {
	if t.Phase == 0 && t.Value == testValueProcessFailure {
		failure, err := agent.NewFailure(agent.FailureKindExecution, "test.otel.failed", "test failure")
		if err != nil {
			return agent.Transition{}, err
		}
		t.Phase = 2
		return agent.Fail(0, failure)
	}
	if t.Phase == 0 && t.Value == testValueStepFailure {
		return agent.Transition{}, errors.New("test Step failure")
	}
	if t.Phase == 0 {
		if t.Value == "pause" {
			t.Phase = 1
			return agent.Pause(0, "test observation restore")
		}
		operation := testEffectObserve
		if t.Value == testValueEffectFailure {
			operation = testEffectFail
		}
		payload, err := json.Marshal(testEffectRequest{Operation: operation})
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewDispatcherEffect(payload)
		if err != nil {
			return agent.Transition{}, err
		}
		t.Phase = 1
		return agent.Continue(0, effect)
	}
	output, err := agent.EncodeOutput(testOutput{Value: t.Value})
	if err != nil {
		return agent.Transition{}, err
	}
	t.Phase = 2
	consumedSignals := uint32(1)
	if t.Value == "pause" {
		consumedSignals = 0
	}
	return agent.Complete(consumedSignals, output)
}

func (t *testExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(struct {
		Value string `json:"value"`
		Phase uint8  `json:"phase"`
	}{Value: t.Value, Phase: t.Phase})
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("test.otel", payload)
}

type testDispatcher struct{}

func (testDispatcher) Dispatch(
	_ context.Context,
	request agent.EffectRequest,
	_ agent.DeltaEmitter,
) (agent.Settlement, error) {
	var effectRequest testEffectRequest
	if err := json.Unmarshal(request.Effect().Payload(), &effectRequest); err != nil {
		return agent.Settlement{}, err
	}
	status := agent.SettlementStatusSucceeded
	if effectRequest.Operation == testEffectFail {
		status = agent.SettlementStatusFailed
	}
	payload, err := json.Marshal(testEffectResponse{OK: status == agent.SettlementStatusSucceeded})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(request.ID(), status, payload)
}

func (testDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicySameIdentity
}

func testDeployment(t *testing.T) agent.Deployment {
	t.Helper()
	return testDeploymentWithDispatcher(t, testDispatcher{})
}

func testDeploymentWithDispatcher(t *testing.T, dispatcher agent.Dispatcher) agent.Deployment {
	t.Helper()
	inputSchema, err := agent.SchemaFor[testInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := agent.SchemaFor[testOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "test.otel", Description: "Exercises the OpenTelemetry Event adapter.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: testDefinition{descriptor: descriptor}, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("test-otel-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("test-otel-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func captureProcessStartedEvent(t *testing.T) agent.Event {
	t.Helper()
	var started agent.Event
	engine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{
		agent.EventListenerFunc(func(_ context.Context, event agent.Event) {
			if event.Name() == agent.EventProcessStarted {
				started = event
			}
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(testInput{Value: "capture start"})
	if err != nil {
		t.Fatal(err)
	}
	if result, runErr := engine.Run(context.Background(), testDeployment(t), input); runErr != nil || !result.Valid() {
		t.Fatalf("capture run result = %#v, error = %v", result, runErr)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if !started.Valid() {
		t.Fatal("Process started event was not captured")
	}
	return started
}

func spansByName(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var matched []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == name {
			matched = append(matched, span)
		}
	}
	return matched
}

func spanByName(t *testing.T, spans []sdktrace.ReadOnlySpan, name string, index int) sdktrace.ReadOnlySpan {
	t.Helper()
	matched := spansByName(spans, name)
	if index >= len(matched) {
		t.Fatalf("span %q index %d is missing", name, index)
	}
	return matched[index]
}

func stringAttribute(attributes []attribute.KeyValue, key string) string {
	for _, value := range attributes {
		if string(value.Key) == key {
			return value.Value.AsString()
		}
	}
	return ""
}

func assertSpanException(t *testing.T, span sdktrace.ReadOnlySpan, wantMessage string) {
	t.Helper()
	if span.Status().Code != codes.Error {
		t.Fatalf("span %q status = %s, want Error", span.Name(), span.Status().Code)
	}
	for _, event := range span.Events() {
		if event.Name != semconv.ExceptionEventName {
			continue
		}
		exceptionType := stringAttribute(event.Attributes, string(semconv.ExceptionTypeKey))
		message := stringAttribute(event.Attributes, string(semconv.ExceptionMessageKey))
		if exceptionType == "" || message != wantMessage {
			t.Fatalf(
				"span %q exception type=%q message=%q, want message %q",
				span.Name(), exceptionType, message, wantMessage,
			)
		}
		return
	}
	t.Fatalf("span %q has no exception event", span.Name())
}

func metricByName(t *testing.T, metrics metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, candidate := range scope.Metrics {
			if candidate.Name == name {
				return candidate
			}
		}
	}
	t.Fatalf("metric %q is missing", name)
	return metricdata.Metrics{}
}

func int64Sum(t *testing.T, metrics metricdata.Metrics) int64 {
	t.Helper()
	data, ok := metrics.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q data = %T, want int64 sum", metrics.Name, metrics.Data)
	}
	var total int64
	for _, point := range data.DataPoints {
		total += point.Value
	}
	return total
}

func assertSumAttribute(t *testing.T, metrics metricdata.Metrics, key, want string) {
	t.Helper()
	data, ok := metrics.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q data = %T, want int64 sum", metrics.Name, metrics.Data)
	}
	for _, point := range data.DataPoints {
		value, found := point.Attributes.Value(attribute.Key(key))
		if found && value.AsString() == want {
			return
		}
	}
	t.Fatalf("metric %q attribute %s = %q is missing", metrics.Name, key, want)
}

func histogramCount(t *testing.T, metrics metricdata.Metrics) uint64 {
	t.Helper()
	data, ok := metrics.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("metric %q data = %T, want float64 histogram", metrics.Name, metrics.Data)
	}
	var total uint64
	for _, point := range data.DataPoints {
		total += point.Count
	}
	return total
}

func assertInt64HistogramSum(t *testing.T, metrics metricdata.Metrics, want int64) {
	t.Helper()
	data, ok := metrics.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("metric %q data = %T, want int64 histogram", metrics.Name, metrics.Data)
	}
	var total int64
	for _, point := range data.DataPoints {
		total += point.Sum
	}
	if total != want {
		t.Fatalf("metric %q sum = %d, want %d", metrics.Name, total, want)
	}
}

func assertHistogramAttribute(t *testing.T, metrics metricdata.Metrics, key, want string) {
	t.Helper()
	data, ok := metrics.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("metric %q data = %T, want float64 histogram", metrics.Name, metrics.Data)
	}
	for _, point := range data.DataPoints {
		value, found := point.Attributes.Value(attribute.Key(key))
		if found && value.AsString() == want {
			return
		}
	}
	t.Fatalf("metric %q attribute %s = %q is missing", metrics.Name, key, want)
}
