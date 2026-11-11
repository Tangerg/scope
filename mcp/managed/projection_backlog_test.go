package managed

import (
	"context"
	"iter"
	"sync"
	"testing"
	"time"

	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

const pendingProjectionKind = "test.pending-projection"

type projectionCountingStore struct {
	store         *run.MemoryStore
	mu            sync.Mutex
	counts        map[run.LSN]int
	afterSnapshot func()
	afterDrain    func()
	drainThrough  run.LSN
}

func (p *projectionCountingStore) Append(ctx context.Context, b run.Batch) error {
	return p.store.Append(ctx, b)
}

func (p *projectionCountingStore) Read(ctx context.Context, id run.ProcessID, after run.LSN) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) {
		p.mu.Lock()
		var callback func()
		// Observe buffers each Read batch, so only its next cursor proves that
		// the consumer has drained every previously returned entry.
		if p.afterDrain != nil && p.drainThrough != 0 && after >= p.drainThrough {
			callback = p.afterDrain
			p.afterDrain = nil
		}
		p.mu.Unlock()
		if callback != nil {
			callback()
		}
		for entry, err := range p.store.Read(ctx, id, after) {
			p.mu.Lock()
			if p.afterDrain != nil {
				p.drainThrough = max(p.drainThrough, entry.LSN)
			}
			p.mu.Unlock()
			if !yield(entry, err) || err != nil {
				return
			}
		}
	}
}

func (p *projectionCountingStore) Load(ctx context.Context, id run.ProcessID) (run.Log, bool, error) {
	log, found, err := p.store.Load(ctx, id)
	p.mu.Lock()
	p.counts[log.Head()]++
	callback := p.afterSnapshot
	p.afterSnapshot = nil
	p.mu.Unlock()
	if callback != nil {
		callback()
	}
	return log, found, err
}

func (p *projectionCountingStore) maximumRepeatedHead() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	maximum := 0
	for _, count := range p.counts {
		maximum = max(maximum, count)
	}
	return maximum
}

type pendingProjectionDefinition struct{ descriptor run.Descriptor }

func (p pendingProjectionDefinition) Descriptor() run.Descriptor { return p.descriptor }

func (p pendingProjectionDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (p pendingProjectionDefinition) Start(run.Payload) (run.Execution, error) {
	return &pendingProjectionExecution{}, nil
}

func (p pendingProjectionDefinition) Restore(_ context.Context, s run.ExecutionState) (run.Execution, error) {
	v, err := s.Decode[pendingProjectionExecution](pendingProjectionKind)
	return &v, err
}

type pendingProjectionExecution struct {
	Started bool `json:"started"`
}

func (p *pendingProjectionExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(pendingProjectionKind, p)
}

func (p *pendingProjectionExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	if !p.Started {
		p.Started = true
		effect, err := run.NewDispatcherEffect([]byte(`null`))
		if err != nil {
			return run.Transition{}, err
		}
		return run.Yield(0, []run.Effect{effect})
	}
	output, err := run.EncodePayload("done")
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(uint32(len(turn.Messages)), output)
}

type pendingProjectionDispatcher struct {
	entered chan struct{}
	release chan struct{}
}

func (p pendingProjectionDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func (p pendingProjectionDispatcher) Dispatch(ctx context.Context, _ run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	close(p.entered)
	select {
	case <-p.release:
		return run.NewSettlement(run.SettlementStatusSucceeded, []byte(`null`))
	case <-ctx.Done():
		return run.Settlement{}, ctx.Err()
	}
}

func TestObserveSkipsCoveredEntriesAndObservesLaterCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store := &projectionCountingStore{store: run.NewMemoryStore(), counts: map[run.LSN]int{}}
	runtime, err := run.New(ctx, run.Config{Build: "projection-backlog", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(context.WithoutCancel(ctx)); closeErr != nil {
			t.Error(closeErr)
		}
	})
	schema, _ := jsonschema.For[string]()
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: pendingProjectionKind, Description: "Backlog projection", InputSchema: schema, OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := pendingProjectionDispatcher{entered: make(chan struct{}), release: make(chan struct{})}
	program, err := run.Bind[string, string](runtime.Compiler(), run.BindingConfig{Definition: pendingProjectionDefinition{descriptor}, Dispatcher: dispatch, Revision: "test"})
	if err != nil {
		t.Fatal(err)
	}
	id := run.StartID{Scope: "test", Key: "backlog"}
	local, err := runtime.Start(ctx, program, run.StartRequest[string]{ID: id, Input: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-dispatch.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	b := &binding[string, string]{cfg: Config[string, string]{
		Runtime: runtime, Program: program,
		Project: func(value string) (tool.Output, error) { return tool.NewTextOutput(value), nil },
	}}
	store.mu.Lock()
	store.afterSnapshot = func() {
		for range 10 {
			if pauseErr := local.Pause(ctx, "projection backlog"); pauseErr != nil {
				t.Error(pauseErr)
				return
			}
			if resumeErr := local.Resume(ctx); resumeErr != nil {
				t.Error(resumeErr)
				return
			}
		}
		store.mu.Lock()
		store.afterDrain = func() {
			close(dispatch.release)
			if _, awaitErr := local.Await(ctx); awaitErr != nil {
				t.Error(awaitErr)
			}
		}
		store.mu.Unlock()
	}
	store.mu.Unlock()
	result, err := observe(ctx, b, local, id, nil)
	if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("later completion was not observed: result=%+v error=%v", result, err)
	}
	if content, ok := result.Content[0].(*sdk.TextContent); !ok || content.Text != "done" {
		t.Fatalf("wrong completion content: %+v", result.Content[0])
	}
	if repeated := store.maximumRepeatedHead(); repeated > 2 {
		t.Fatalf("MCP observer loaded/refolded one fixed committed head %d times for 20 backlog entries", repeated)
	}
}
