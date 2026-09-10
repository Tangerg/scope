package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

// A paused receiver permits next-boundary input without consuming it. Its pure
// transition lets the durability test distinguish admission from adoption.
type pausedDefinition struct{ descriptor agent.Descriptor }

func pausedWorker() agent.Deployment {
	schema := require(agent.SchemaFor[string]())
	return binding(&pausedDefinition{descriptor: require(agent.NewDescriptor(agent.DescriptorConfig{Name: "test.paused", Description: "Pause before consuming a signal.", InputSchema: schema, OutputSchema: schema}))})
}
func (p *pausedDefinition) Descriptor() agent.Descriptor { return p.descriptor }
func (p *pausedDefinition) Start(input agent.Input) (agent.Execution, error) {
	if err := p.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &pausedExecution{}, nil
}
func (p *pausedDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	var execution pausedExecution
	if state.Kind() != "test.paused" {
		return nil, errors.New("unexpected state")
	}
	if err := json.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

type pausedExecution struct {
	Ready bool `json:"ready"`
}

func (p *pausedExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	if !p.Ready {
		p.Ready = true
		return agent.Pause(0, "Waiting for host resume.")
	}
	if len(signals) != 1 {
		return agent.Transition{}, errors.New("one signal required")
	}
	return agent.Complete(1, require(agent.ParseOutput(signals[0].Payload())))
}
func (p *pausedExecution) Snapshot() (agent.ExecutionState, error) {
	return agent.NewExecutionState("test.paused", require(json.Marshal(p)))
}

type heldControlDurability struct {
	*agenttest.MemoryTreeDurability
	operation string
	entered   chan agent.EffectBoundary
	release   chan struct{}
	once      sync.Once
}

func (h *heldControlDurability) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	if err := h.MemoryTreeDurability.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	var payload struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(boundary.Request().Effect().Payload(), &payload); err != nil {
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
				definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
					switch turn.Number {
					case 1:
						return Decision{Mode: Continue, State: turn.State, Tasks: []TaskRequest{request("receiver", "test.paused", "work")}}, nil
					case 2:
						signal := require(agent.NewSignalRequest(require(agent.ParseSignalID("signal:durable-control")), agent.WaitID{}, []byte(`"direction"`)))
						reason := "Stop receiver."
						return Decision{Mode: Wait, State: turn.State, Controls: []Control{{Task: turn.Tasks[0].Request.Key, Signal: &signal}, {Task: turn.Tasks[0].Request.Key, CancelReason: &reason}}}, nil
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
				store := &heldControlDurability{MemoryTreeDurability: agenttest.NewMemoryTreeDurability(), operation: operation, entered: make(chan agent.EffectBoundary, 1), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(store.release) })
				defer release()
				engine, process := run(t, definition, deployments, store)
				boundary := <-store.entered
				synctest.Wait()
				if boundary.Kind() != agent.EffectBoundarySettled {
					t.Fatal("control used an external pending permission")
				}
				inspection := require(engine.InspectTree(t.Context(), process.ID()))
				if !inspection.CommitPending || inspection.HeadDigest != boundary.PreviousTreeDigest() {
					t.Fatal("prospective control published before acknowledgment")
				}
				var receiver agent.ProcessSnapshot
				for _, snapshot := range boundary.TreeSnapshot().ProcessSnapshots() {
					if snapshot.DeploymentRef().Name() == "test.paused" {
						receiver = snapshot
					}
				}
				if !receiver.Valid() || receiver.Usage().AcceptedSignals != 1 || len(receiver.SignalReceipts()) != 1 || receiver.SignalReceipts()[0].Consumed() {
					t.Fatal("prospective admission lost identity or consumed early")
				}
				head, present, err := store.LoadTree(t.Context(), process.ID())
				if err != nil || !present || head.Digest() != boundary.TreeSnapshot().Digest() {
					t.Fatalf("head: %t %v", present, err)
				}
				restoredEngine := require(agent.NewEngine(agent.EngineConfig{TreeDurability: store, DeploymentResolver: deployments}))
				t.Cleanup(func() {
					if err := restoredEngine.Close(context.Background()); err != nil {
						t.Error(err)
					}
				})
				restored := require(restoredEngine.RestoreTree(t.Context(), binding(definition), head))
				release()
				if got := completed(t, restored); got != "recovered once" {
					t.Fatal(got)
				}
				final := require(restoredEngine.InspectTree(t.Context(), process.ID()))
				for _, fact := range final.Processes {
					if fact.Snapshot.DeploymentRef().Name() == "test.paused" && fact.Snapshot.Usage().AcceptedSignals != 1 {
						t.Fatal("restoration delivered twice")
					}
				}
			})
		})
	}
}
