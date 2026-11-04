package run

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestCheckpointPriorityDoesNotDependOnAnEmptyMailbox(t *testing.T) {
	for _, test := range []struct {
		name       string
		cancelHost bool
		stop       bool
		wantRecord RecordKind
	}{
		{name: "checkpoint", wantRecord: RecordKindCheckpoint},
		{name: "host cancellation", cancelHost: true, wantRecord: RecordKindControlRequested},
		{name: "writer stop", stop: true},
		{name: "cancellation before writer stop", cancelHost: true, stop: true, wantRecord: RecordKindControlRequested},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store, CheckpointInterval: 1})
			input := controlValue(EncodePayload(0))
			effect := controlValue(NewDispatcherEffect(input.JSON()))
			settlement := controlValue(NewSettlement(SettlementStatusSucceeded, input.JSON()))
			transition := controlValue(Yield(0, []Effect{effect}))
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{
				Definition: &boundaryDefinition{descriptor: kernelDescriptor(t, "test.checkpoint_priority"), step: func(context.Context, Turn) (Transition, error) {
					return transition, nil
				}},
				Dispatcher: boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
					return settlement, nil
				}}, Revision: "checkpoint",
			}))
			root := hostStartID().Root()
			started := Started{Process: root, Program: program.ProgramRef(), Input: input, Limits: DefaultTreeLimits(), At: time.Unix(1, 0).UTC()}
			initial := Batch{Root: root, Records: []Record{started, Activated{Epoch: 1}}}
			intent := EffectIntent{ID: EffectID{Process: root, Step: 1}, Effect: effect, Policy: EffectPolicy{Replay: ReplayPolicyNever}, At: started.At}
			committed := Batch{Root: root, Epoch: 1, After: 2, Records: []Record{Stepped{Process: root, Step: 1, Transition: transition}, intent}}
			for _, batch := range []Batch{initial, committed} {
				if err := store.Append(t.Context(), batch); err != nil {
					t.Fatal(err)
				}
			}
			log, found, err := store.Load(t.Context(), root)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			state := controlValue(runtime.restore(t.Context(), program.Erase(), log))
			host, cancel := context.WithCancel(t.Context())
			defer cancel()
			tree := newTreeRuntime(runtime, state, host)
			defer tree.stop()
			tree.checkpointAfter = 1
			_, stopDispatch := context.WithCancel(tree.ctx)
			defer stopDispatch()
			tree.jobs[intent.ID] = &dispatchJob{cancel: stopDispatch}
			tree.commands <- treeCommand{ctx: t.Context(), process: root, operation: controlCommand{kind: ControlPause, reason: "queued pause"}, reply: make(chan commandResponse, 1)}
			tree.results <- dispatchResult{id: intent.ID, outcome: settlement}
			events := treeEvents{host: host.Done(), stop: tree.stopRequested}
			if test.cancelHost {
				cancel()
			}
			if test.stop {
				tree.stop()
			}
			if !tree.servePending(&events) || tree.failure != nil {
				t.Fatalf("priority action failed: %v", tree.failure)
			}
			if len(tree.commands) != 1 || len(tree.results) != 1 || tree.stopping != test.stop {
				t.Fatalf("mailbox displaced the priority action: commands=%d results=%d stopping=%v", len(tree.commands), len(tree.results), tree.stopping)
			}
			var records []RecordKind
			for entry, err := range store.Read(t.Context(), root, log.Head()) {
				if err != nil {
					t.Fatal(err)
				}
				records = append(records, entry.Record.recordKind())
			}
			if test.wantRecord == "" {
				if len(records) != 0 {
					t.Fatalf("writer stop published records: %v", records)
				}
			} else if len(records) != 1 || records[0] != test.wantRecord {
				t.Fatalf("priority records=%v, want %s", records, test.wantRecord)
			}
		})
	}
}

func TestCheckpointDrainsRunningStepsBeforeStartingMore(t *testing.T) {
	for _, outcome := range []Status{StatusCompleted, StatusFailed, StatusCanceled} {
		t.Run(outcome.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := NewMemoryStore()
				runtime := kernelRuntime(t, Config{Store: store, CheckpointInterval: 1})
				slowEntered, fastEntered, releaseSlow := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var fastSteps atomic.Uint64
				slow := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{
					Definition: &boundaryDefinition{descriptor: kernelDescriptor(t, "test.checkpoint_slow"), step: func(ctx context.Context, _ Turn) (Transition, error) {
						close(slowEntered)
						// The gate controls scheduling; it never selects transition data.
						select {
						case <-ctx.Done():
							return Transition{}, ctx.Err()
						case <-releaseSlow:
						}
						if outcome == StatusFailed {
							return Transition{}, errors.New("checkpoint fixture step failed")
						}
						return Complete(0, controlValue(EncodePayload(1)))
					}}, Revision: "checkpoint",
				}))
				fast := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{
					Definition: &boundaryDefinition{descriptor: kernelDescriptor(t, "test.checkpoint_fast"), step: func(ctx context.Context, turn Turn) (Transition, error) {
						fastSteps.Add(1)
						if turn.Step > 1 {
							return Complete(uint32(len(turn.Messages)), controlValue(EncodePayload(2)))
						}
						select {
						case <-ctx.Done():
							return Transition{}, ctx.Err()
						case <-slowEntered:
						}
						close(fastEntered)
						return Yield(0, []Effect{controlValue(NewDispatcherEffect(controlValue(EncodePayload(0)).JSON()))})
					}},
					Dispatcher: boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
						return NewSettlement(SettlementStatusSucceeded, controlValue(EncodePayload(0)).JSON())
					}}, Revision: "checkpoint",
				}))
				parent := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{
					Definition: &checkpointParentDefinition{
						fixture:     treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.checkpoint_parent"), children: []Program[Payload, Payload]{slow.Erase(), fast.Erase()}},
						fastEntered: fastEntered,
					}, Revision: "checkpoint",
				}))
				ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
				defer cancel()
				handle := controlValue(runtime.Start(ctx, parent, StartRequest[int]{ID: hostStartID()}))
				synctest.Wait()
				if got := fastSteps.Load(); got != 1 {
					t.Fatalf("started %d fast Steps while a due checkpoint waited for its running sibling", got)
				}
				fastID := controlValue(handle.ID().Child(controlValue(ParseChildKey("child-1"))))
				inspection := controlValue(handle.Inspect(ctx))
				fastProcess, found := inspection.Process(fastID)
				if !found || fastProcess.Usage != (Usage{CommittedSteps: 1, PreparedEffects: 1, AcceptedMessages: 1}) || len(fastProcess.PendingEffects) != 0 {
					t.Fatalf("checkpoint barrier blocked effect settlement: %+v", fastProcess)
				}
				before := inspection.Head
				if err := handle.Pause(ctx, "exercise checkpoint barrier controls"); err != nil {
					t.Fatal(err)
				}
				if err := handle.Resume(ctx); err != nil {
					t.Fatal(err)
				}
				if outcome == StatusCanceled {
					if err := handle.Cancel(ctx, "cancel while checkpoint drains"); err != nil {
						t.Fatal(err)
					}
				} else {
					close(releaseSlow)
				}
				result := controlValue(handle.Await(ctx))
				wantStatus := StatusCompleted
				if outcome == StatusCanceled {
					wantStatus = StatusCanceled
				}
				if result.Termination().Status() != wantStatus {
					t.Fatalf("terminal result=%v, want %v", result.Termination().Status(), wantStatus)
				}
				checkpointed := false
				for entry, err := range store.Read(ctx, handle.ID(), before) {
					if err != nil {
						t.Fatal(err)
					}
					if _, ok := entry.Record.(Checkpoint); ok {
						checkpointed = true
					}
					if step, ok := entry.Record.(Stepped); ok && step.Process == fastID && !checkpointed {
						t.Fatal("new work committed before the pending checkpoint")
					}
				}
				if !checkpointed {
					t.Fatal("drained Steps did not release the pending checkpoint")
				}
			})
		})
	}
}

func TestCheckpointSnapshotFailureStopsBeforeAnotherStep(t *testing.T) {
	runtime := kernelRuntime(t, Config{CheckpointInterval: 1})
	cause := errors.New("checkpoint snapshot failed")
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{
		Definition: &checkpointFailureDefinition{descriptor: kernelDescriptor(t, "test.checkpoint_failure"), failure: cause}, Revision: "checkpoint",
	}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	if _, err := handle.Await(ctx); !errors.Is(err, cause) {
		t.Fatalf("failed checkpoint did not stop its writer: %v", err)
	}
	history := controlValue(runtime.History(ctx, handle.ID()))
	if history.Head() != 3 || controlValue(history.Usage()) != (Usage{CommittedSteps: 1}) {
		t.Fatalf("failed checkpoint changed acknowledged progress: %+v", history)
	}
	for _, entry := range history.Entries {
		if _, ok := entry.Record.(Checkpoint); ok {
			t.Fatal("failed snapshot was published as a recovery boundary")
		}
	}
}

const checkpointFailureStateKind = "test.checkpoint_failure"

type checkpointFailureDefinition struct {
	descriptor Descriptor
	failure    error
}

func (c *checkpointFailureDefinition) Descriptor() Descriptor { return c.descriptor }
func (*checkpointFailureDefinition) ChildPrograms() []Program[Payload, Payload] {
	return nil
}
func (c *checkpointFailureDefinition) Start(Payload) (Execution, error) {
	return &checkpointFailureExecution{failure: c.failure}, nil
}
func (c *checkpointFailureDefinition) Restore(_ context.Context, state ExecutionState) (Execution, error) {
	advanced, err := state.Decode[bool](checkpointFailureStateKind)
	if err != nil {
		return nil, err
	}
	return &checkpointFailureExecution{advanced: advanced, failure: c.failure}, nil
}

type checkpointFailureExecution struct {
	advanced bool
	failure  error
}

func (c *checkpointFailureExecution) Step(context.Context, Turn) (Transition, error) {
	c.advanced = true
	return Continue(0)
}
func (c *checkpointFailureExecution) Snapshot() (ExecutionState, error) {
	if c.advanced {
		return ExecutionState{}, c.failure
	}
	return EncodeExecutionState(checkpointFailureStateKind, c.advanced)
}

type checkpointParentDefinition struct {
	fixture     treeFixtureDefinition
	fastEntered <-chan struct{}
}

func (c *checkpointParentDefinition) Descriptor() Descriptor { return c.fixture.Descriptor() }
func (c *checkpointParentDefinition) ChildPrograms() []Program[Payload, Payload] {
	return c.fixture.ChildPrograms()
}
func (c *checkpointParentDefinition) Start(input Payload) (Execution, error) {
	fixture, err := c.fixture.Start(input)
	if err != nil {
		return nil, err
	}
	return &checkpointParentExecution{fixture: fixture, fastEntered: c.fastEntered}, nil
}
func (c *checkpointParentDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	fixture, err := c.fixture.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &checkpointParentExecution{fixture: fixture, fastEntered: c.fastEntered}, nil
}

type checkpointParentExecution struct {
	fixture     Execution
	fastEntered <-chan struct{}
}

func (c *checkpointParentExecution) Snapshot() (ExecutionState, error) { return c.fixture.Snapshot() }
func (c *checkpointParentExecution) Step(ctx context.Context, turn Turn) (Transition, error) {
	if turn.Step > 1 {
		select {
		case <-ctx.Done():
			return Transition{}, ctx.Err()
		case <-c.fastEntered:
		}
	}
	return c.fixture.Step(ctx, turn)
}
