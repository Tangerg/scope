package interaction_test

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func TestStreamingOutputDoesNotDependOnDeltaListeners(t *testing.T) {
	collector := &deltaCollector{}
	engine, err := agent.NewEngine(agent.EngineConfig{
		EventListeners: []agent.EventListener{passiveEventListener{}, panickingEventListener{}},
		DeltaListeners: []agent.DeltaListener{collector, passiveDeltaListener{}, panickingDeltaListener{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newStreamingDeployment(t, responseStream(
		streamTextChunk("hel", ""),
		streamTextChunk("lo", chat.FinishReasonStop),
	))
	input := interactionInput(t, "stream")
	result, err := engine.Run(context.Background(), deployment.Deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	observationFailures := engine.ObservationFailures()
	if observationFailures.EventListenerPanics() == 0 ||
		observationFailures.DeltaListenerPanics() == 0 {
		t.Fatalf(
			"observation failures = event %d, delta %d, want both non-zero",
			observationFailures.EventListenerPanics(),
			observationFailures.DeltaListenerPanics(),
		)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	erased, _ := result.Output()
	output, err := erased.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if output.ModelResponse == nil || output.ModelResponse.Text() != "hello" {
		t.Fatalf("final output = %#v, want hello", output)
	}
	chunks := collector.Responses()
	if len(chunks) != 2 || chunks[0].Text() != "hel" || chunks[1].Text() != "lo" {
		t.Fatalf("observed chunks = %#v", chunks)
	}
}

func TestStreamingUsesBoundedBestEffortDeltaQueue(t *testing.T) {
	listener := newBlockingDeltaListener()
	t.Cleanup(listener.Release)
	events := &eventRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{
		EventListeners:      []agent.EventListener{events},
		DeltaListeners:      []agent.DeltaListener{listener},
		DeltaBufferCapacity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if !yield(streamTextChunk("x", ""), nil) {
				return
			}
			<-listener.started
			for range 64 {
				if !yield(streamTextChunk("x", ""), nil) {
					return
				}
			}
			yield(streamTextChunk("x", chat.FinishReasonStop), nil)
		}
	})
	deployment := newStreamingDeployment(t, streamer)
	result, err := engine.Run(context.Background(), deployment.Deployment, interactionInput(t, "bounded"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s", result.Status())
	}
	if result.Usage().DroppedDeltas == 0 {
		t.Fatal("DroppedDeltas = 0, want an observable bounded-queue drop")
	}
	if !events.Contains("agent.delta.dropped") {
		t.Fatal("missing agent.delta.dropped event")
	}
	listener.Release()
	if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	erased, _ := result.Output()
	output, err := erased.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if output.ModelResponse == nil || output.ModelResponse.Text() != strings.Repeat("x", 66) {
		t.Fatalf("final output = %#v, want 66 streamed chunks", output)
	}
}

func TestRestoringCompletedInteractionDoesNotReplayDeltas(t *testing.T) {
	firstEngine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newStreamingDeployment(t, responseStream(streamTextChunk("done", chat.FinishReasonStop)))
	process, err := firstEngine.Start(context.Background(), deployment.Deployment, interactionInput(t, "restore"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firstEngine.CaptureTree(context.Background(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := firstEngine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}

	collector := &deltaCollector{}
	restoredEngine, err := agent.NewEngine(agent.EngineConfig{DeltaListeners: []agent.DeltaListener{collector}})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment.Deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := restored.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if len(collector.Responses()) != 0 {
		t.Fatal("restoration replayed historical model Deltas")
	}
	firstOutput, _ := first.Output()
	secondOutput, _ := second.Output()
	if string(firstOutput.JSON()) != string(secondOutput.JSON()) {
		t.Fatal("restored final Output differs")
	}
}

func newStreamingDeployment(t *testing.T, streamer chat.Streamer) interactionDeployment {
	t.Helper()
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name:          "interaction.stream",
		Description:   "Verify managed streaming Interaction behavior.",
		MaxModelCalls: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Streamer: streamer,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		Dispatcher:           dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("interaction-stream-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("interaction-stream-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return toolInteractionDeployment(deployment, interaction.ToolSet{})
}

func interactionInput(t *testing.T, text string) agent.Input {
	t.Helper()
	input, err := agent.EncodeInput(interaction.Input{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(text))},
	})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func responseStream(chunks ...*chat.ResponseDelta) chat.Streamer {
	return chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			for _, chunk := range chunks {
				if !yield(chunk, nil) {
					return
				}
			}
		}
	})
}

func streamTextChunk(text string, finish chat.FinishReason) *chat.ResponseDelta {
	return &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(text)}, FinishReason: finish}
}

type deltaCollector struct {
	mu     sync.Mutex
	deltas []*chat.ResponseDelta
}

func (d *deltaCollector) OnDelta(_ context.Context, delta agent.Delta) {
	decoded, err := interaction.ParseModelResponseDelta(delta.Payload())
	if err != nil {
		return
	}
	d.mu.Lock()
	d.deltas = append(d.deltas, decoded.ResponseDelta())
	d.mu.Unlock()
}

func (d *deltaCollector) Responses() []*chat.ResponseDelta {
	d.mu.Lock()
	defer d.mu.Unlock()
	deltas := make([]*chat.ResponseDelta, len(d.deltas))
	for index := range d.deltas {
		deltas[index] = d.deltas[index].Clone()
	}
	return deltas
}

type passiveDeltaListener struct{}

func (passiveDeltaListener) OnDelta(context.Context, agent.Delta) {}

type panickingDeltaListener struct{}

func (panickingDeltaListener) OnDelta(context.Context, agent.Delta) {
	panic("delta listener panicked")
}

type passiveEventListener struct{}

func (passiveEventListener) OnEvent(context.Context, agent.Event) {}

type panickingEventListener struct{}

func (panickingEventListener) OnEvent(context.Context, agent.Event) {
	panic("event listener panicked")
}

type blockingDeltaListener struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newBlockingDeltaListener() *blockingDeltaListener {
	return &blockingDeltaListener{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingDeltaListener) OnDelta(context.Context, agent.Delta) {
	b.startedOnce.Do(func() { close(b.started) })
	<-b.release
}

func (b *blockingDeltaListener) Release() {
	b.releaseOnce.Do(func() { close(b.release) })
}

type eventRecorder struct {
	mu    sync.Mutex
	names []string
}

func (e *eventRecorder) OnEvent(_ context.Context, event agent.Event) {
	e.mu.Lock()
	e.names = append(e.names, event.Name())
	e.mu.Unlock()
}

func (e *eventRecorder) Contains(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Contains(e.names, name)
}

func TestDispatcherRequiresExactlyOneModelCapability(t *testing.T) {
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "capability", Description: "Validate model capabilities.", MaxModelCalls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		t.Fatal("construction invoked model")
		return nil, nil
	})
	streamer := responseStream(streamTextChunk("done", chat.FinishReasonStop))
	for name, config := range map[string]interaction.DispatcherConfig{
		"neither": {}, "both": {Model: model, Streamer: streamer},
		"nil model": {Model: chat.ModelFunc(nil)}, "nil streamer": {Streamer: chat.StreamerFunc(nil)},
		"nil model with streamer": {Model: chat.ModelFunc(nil), Streamer: streamer},
		"model with nil streamer": {Model: model, Streamer: chat.StreamerFunc(nil)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := interaction.NewDispatcher(definition, config); !errors.Is(err, interaction.ErrInvalidDispatcherConfig) {
				t.Fatalf("NewDispatcher error = %v", err)
			}
		})
	}
	for _, config := range []interaction.DispatcherConfig{{Model: model}, {Streamer: streamer}} {
		if _, err := interaction.NewDispatcher(definition, config); err != nil {
			t.Fatal(err)
		}
	}
}
