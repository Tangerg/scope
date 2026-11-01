package team

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// A paused receiver permits next-boundary input without consuming it. Its pure
// transition lets the committer test distinguish admission from adoption.
type pausedDefinition struct{ descriptor agentrun.Descriptor }

func pausedWorker() agentrun.Program[agentrun.Payload, agentrun.Payload] {
	schema := require(jsonschema.For[string]())
	return binding(&pausedDefinition{descriptor: require(agentrun.NewDescriptor(agentrun.DescriptorConfig{Name: "test.paused", Description: "Pause before consuming a signal.", InputSchema: schema, OutputSchema: schema, SignalSchema: schema}))})
}
func (p *pausedDefinition) Descriptor() agentrun.Descriptor { return p.descriptor }

func (*pausedDefinition) ChildDeployments() []agentrun.Program[agentrun.Payload, agentrun.Payload] {
	return nil
}
func (p *pausedDefinition) Start(input agentrun.Payload) (agentrun.Execution, error) {
	if err := p.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &pausedExecution{}, nil
}
func (p *pausedDefinition) Restore(ctx context.Context, state agentrun.ExecutionState) (agentrun.Execution, error) {
	var execution pausedExecution
	if state.Kind() != "test.paused" {
		return nil, errors.New("unexpected state")
	}
	if err := jsonv2.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

type pausedExecution struct {
	Ready bool `json:"ready"`
}

func (p *pausedExecution) Step(ctx context.Context, signals []agentrun.Signal) (agentrun.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agentrun.Transition{}, err
	}
	if !p.Ready {
		p.Ready = true
		return agentrun.Pause(0, "Waiting for host resume.")
	}
	if len(signals) != 1 {
		return agentrun.Transition{}, errors.New("one signal required")
	}
	return agentrun.Complete(1, require(agentrun.ParsePayload(signals[0].Payload())))
}
func (p *pausedExecution) Snapshot() (agentrun.ExecutionState, error) {
	return agentrun.ParseExecutionState("test.paused", require(jsonv2.Marshal(p)))
}

type heldControlDurability struct {
	*agentrun.MemoryStore
	operation string
	entered   chan agentrun.EffectBoundary
	release   chan struct{}
	once      sync.Once
}

func (h *heldControlDurability) CommitEffect(ctx context.Context, boundary agentrun.EffectBoundary) error {
	if err := h.MemoryStore.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	var payload struct {
		Operation string `json:"operation"`
	}
	if err := jsonv2.Unmarshal(boundary.Request().Effect().Payload(), &payload); err != nil {
		return err
	}
	if payload.Operation == h.operation {
		h.once.Do(func() { h.entered <- boundary; <-h.release })
	}
	return nil
}

func TestControlAdmissionAndReceiptRecoverAsOneTreeCut(t *testing.T) {
	for _, operation := range []string{"signal_child", "cancel_child"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				definition := fixture(func(_ context.Context, turn Turn) (Decision, error) {
					switch turn.Number {
					case 1:
						return Decision{Mode: ModeContinue, State: turn.State, Tasks: []TaskRequest{request("receiver", "test.paused", "work")}}, nil
					case 2:
						signal := require(agentrun.NewSignalRequest(require(agentrun.ParseSignalID("signal:durable-control")), agentrun.WaitID{}, []byte(`"direction"`)))
						reason := "Stop receiver."
						return Decision{Mode: ModeWait, State: turn.State, Controls: []Control{{Task: turn.Tasks[0].Request.Key, Signal: &signal}, {Task: turn.Tasks[0].Request.Key, CancelReason: &reason}}}, nil
					default:
						if turn.Number != 3 || len(turn.Controls) != 2 || turn.Tasks[0].Outcome == nil {
							return Decision{}, errors.New("incomplete recovery")
						}
						for _, receipt := range turn.Controls {
							if receipt.Result == nil {
								return Decision{}, errors.New("missing control receipt")
							}
							if _, failed := receipt.Result.Failure(); failed {
								return Decision{}, errors.New("control rejected")
							}
						}
						return finish(turn, "recovered once"), nil
					}
				}, pausedWorker())
				store := &heldControlDurability{MemoryStore: agentrun.NewMemoryStore(), operation: operation, entered: make(chan agentrun.EffectBoundary, 1), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(store.release) })
				defer release()
				engine, process := run(t, definition, store)
				boundary := <-store.entered
				synctest.Wait()
				if boundary.Kind() != agentrun.EffectBoundaryKindSettled {
					t.Fatal("control used an external pending permission")
				}
				inspection := require(engine.InspectTree(t.Context(), process.Relation().ProcessID()))
				if !inspection.CommitPending || inspection.HeadDigest != boundary.PreviousTreeDigest() {
					t.Fatal("prospective control published before acknowledgment")
				}
				var receiver agentrun.ProcessSnapshot
				for _, snapshot := range boundary.TreeSnapshot().ProcessSnapshots() {
					if snapshot.ProgramRef().Name() == "test.paused" {
						receiver = snapshot
					}
				}
				if !receiver.Valid() || receiver.Usage().AcceptedSignals != 1 || len(receiver.SignalReceipts()) != 1 || receiver.SignalReceipts()[0].Consumed() {
					t.Fatal("prospective admission lost identity or consumed early")
				}
				head, present, err := store.LoadTree(t.Context(), process.Relation().ProcessID())
				if err != nil || !present || head.Digest() != boundary.TreeSnapshot().Digest() {
					t.Fatalf("head: %t %v", present, err)
				}
				restoredEngine := require(agentrun.New(t.Context(), agentrun.Config{Store: store}))
				t.Cleanup(func() {
					if err := restoredEngine.Close(context.Background()); err != nil {
						t.Error(err)
					}
				})
				restored := require(restoredEngine.Recover(t.Context(), binding(definition), head))
				release()
				if got := completed(t, restored); got != "recovered once" {
					t.Fatal(got)
				}
				final := require(restoredEngine.InspectTree(t.Context(), process.Relation().ProcessID()))
				for _, fact := range final.Processes {
					if fact.Snapshot.ProgramRef().Name() == "test.paused" && fact.Snapshot.Usage().AcceptedSignals != 1 {
						t.Fatal("restoration delivered twice")
					}
				}
			})
		})
	}
}
