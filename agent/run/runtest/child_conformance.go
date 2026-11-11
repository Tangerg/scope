package runtest

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func runChildConformance(t *testing.T, factory func(*testing.T) run.Store) {
	t.Run("reaped children stay absent after recovery", func(t *testing.T) { checkReapRecovery(t, factory(t)) })
	t.Run("descendant exits precede their parents", func(t *testing.T) { checkChildExitOrder(t, factory(t)) })
	t.Run("one duplicate child key succeeds", func(t *testing.T) { checkDuplicateChildKey(t, factory(t)) })
	t.Run("reaped child paths cannot be reused", func(t *testing.T) { checkReapedChildKey(t, factory(t)) })
	t.Run("canceling joins blocked child admission", func(t *testing.T) { checkChildAdmissionCancellation(t, factory(t)) })
}

func checkChildAdmissionCancellation(t *testing.T, store run.Store) {
	entered, returned := make(chan struct{}), make(chan struct{})
	admitter := run.ProcessAdmitterFunc(func(ctx context.Context, admission run.ProcessAdmission) error {
		if admission.ProcessID().Depth() == 0 {
			return nil
		}
		close(entered)
		defer close(returned)
		<-ctx.Done()
		return ctx.Err()
	})
	runtime, program := childRuntime(t, run.Config{Store: store, ProcessAdmitter: admitter}, childScenario{Depth: 1})
	ctx, cancel := context.WithTimeout(t.Context(), conformanceTimeout)
	defer cancel()
	handle, err := runtime.Start(ctx, program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "cancel-child-admission"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("child admission was not called")
	}
	if cancelErr := handle.Cancel(ctx, "stop the child admission"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	result, err := handle.Await(ctx)
	if err != nil || result.Termination().Status() != run.StatusCanceled {
		t.Fatalf("root canceled status=%s error=%v", result.Termination().Status(), err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("root Exit preceded its child admission callback returning")
	}
	log := loadStoreLog(t, store, handle.ID())
	rejected := 0
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Started:
			if record.Process.Depth() != 0 {
				t.Fatal("canceled admission started its child")
			}
		case run.EffectSettled:
			if record.Outcome.Status() != run.SettlementStatusFailed {
				t.Fatal("canceled local Spawn did not have a definite rejection")
			}
			rejected++
		}
	}
	if rejected != 1 {
		t.Fatalf("Spawn rejection count=%d, want 1", rejected)
	}
}

func checkReapRecovery(t *testing.T, store run.Store) {
	gate := newAppendBarrier(store, run.RecordKindReaped, cutAfterAppend)
	defer gate.abort()
	runtime, program := childRuntime(t, run.Config{Store: gate}, childScenario{Depth: 1})
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "reap"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	batch := gate.await(t)
	var child run.ProcessID
	for _, record := range batch.Records {
		if reaped, ok := record.(run.Reaped); ok {
			child = reaped.Process
		}
	}
	if !child.Valid() {
		t.Fatal("reap barrier lost its child")
	}
	recoveredRuntime, recoveredProgram := childRuntime(t, run.Config{Store: store}, childScenario{Depth: 1})
	recovered, err := recoveredRuntime.Recover(t.Context(), recoveredProgram, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := recovered.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	inspection, err := recovered.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := inspection.Process(child); exists {
		t.Fatal("recovery resurrected a reaped child")
	}
	if len(inspection.Processes) != 1 {
		t.Fatalf("active materialization retained %d processes, want root only", len(inspection.Processes))
	}
	gate.abort()
	if _, err := handle.Await(t.Context()); !errors.Is(err, errConformanceCrash) {
		t.Fatal(err)
	}
}

func checkChildExitOrder(t *testing.T, store run.Store) {
	runtime, program := childRuntime(t, run.Config{Store: store}, childScenario{Depth: 2})
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "structured"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	log := loadStoreLog(t, store, handle.ID())
	var exits []run.ProcessID
	for _, entry := range log.Entries {
		if exited, ok := entry.Record.(run.Exited); ok {
			exits = append(exits, exited.Process)
		}
	}
	if len(exits) != 3 {
		t.Fatalf("exit count=%d", len(exits))
	}
	for index, depth := range []uint32{2, 1, 0} {
		if exits[index].Depth() != depth {
			t.Fatalf("exit order=%v", exits)
		}
	}
}

func checkDuplicateChildKey(t *testing.T, store run.Store) {
	runtime, program := childRuntime(t, run.Config{Store: store}, childScenario{Depth: 1, Duplicate: true})
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "duplicate-child"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handle.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("duplicate key outcome=%s error=%v", result.Termination().Status(), err)
	}
	log := loadStoreLog(t, store, handle.ID())
	children, successes, failures := 0, 0, 0
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Started:
			if record.Process.Depth() == 1 {
				children++
			}
		case run.EffectSettled:
			if record.ID.Process != handle.ID() {
				continue
			}
			switch record.Outcome.Status() {
			case run.SettlementStatusSucceeded:
				successes++
			case run.SettlementStatusFailed:
				failures++
			}
		}
	}
	if children != 1 || successes != 1 || failures != 1 {
		t.Fatalf("children=%d successes=%d failures=%d", children, successes, failures)
	}
}

func checkReapedChildKey(t *testing.T, store run.Store) {
	runtime, program := childRuntime(t, run.Config{Store: store}, childScenario{Depth: 1, Reuse: true})
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "reuse-child"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handle.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("reused path outcome=%s error=%v", result.Termination().Status(), err)
	}
	log := loadStoreLog(t, store, handle.ID())
	children := 0
	for _, entry := range log.Entries {
		if started, ok := entry.Record.(run.Started); ok && started.Process.Depth() == 1 {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("same path was started %d times", children)
	}
}

type childScenario struct {
	Depth     int
	Duplicate bool
	Reuse     bool
}

func childRuntime(t *testing.T, config run.Config, scenario childScenario) (*run.Runtime, run.Program[run.Payload, run.Payload]) {
	t.Helper()
	config.Build = conformanceBuild
	runtime, err := run.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), conformanceTimeout)
		defer cancel()
		if closeErr := runtime.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	leaf := newRecoveryDefinition(t, recoveryComplete)
	program, err := run.Bind[run.Payload, run.Payload](runtime.Compiler(), run.BindingConfig{Definition: leaf, Revision: "one", Configuration: leaf.mode})
	if err != nil {
		t.Fatal(err)
	}
	for level := range scenario.Depth {
		definition := newChildDefinition(t, program, scenario.Duplicate && level == scenario.Depth-1)
		definition.reuse = scenario.Reuse && level == scenario.Depth-1
		program, err = run.Bind[run.Payload, run.Payload](runtime.Compiler(), run.BindingConfig{Definition: definition, Revision: "one", Configuration: scenario})
		if err != nil {
			t.Fatal(err)
		}
	}
	return runtime, program
}

type childDefinition struct {
	descriptor run.Descriptor
	child      run.Program[run.Payload, run.Payload]
	duplicate  bool
	reuse      bool
}

func newChildDefinition(t *testing.T, child run.Program[run.Payload, run.Payload], duplicate bool) *childDefinition {
	t.Helper()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "runtest.child", Description: "Exercise structured child ownership.", InputSchema: schema, OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	return &childDefinition{descriptor: descriptor, child: child, duplicate: duplicate}
}

func (c *childDefinition) Descriptor() run.Descriptor { return c.descriptor }

func (c *childDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return []run.Program[run.Payload, run.Payload]{c.child}
}

func (c *childDefinition) Start(input run.Payload) (run.Execution, error) {
	return &childExecution{definition: c, state: childState{Input: input}}, nil
}

func (c *childDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[childState]("runtest.child")
	if err != nil {
		return nil, err
	}
	return &childExecution{definition: c, state: value}, nil
}

type childState struct {
	Input     run.Payload `json:"input"`
	Started   bool        `json:"started"`
	Completed uint32      `json:"completed"`
	Exited    bool        `json:"exited"`
	Retry     bool        `json:"retry"`
}

type childExecution struct {
	definition *childDefinition
	state      childState
}

func (c *childExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	consumed := uint32(len(turn.Messages))
	count := uint32(1)
	if c.definition.duplicate {
		count = 2
	}
	if !c.state.Started {
		c.state.Started = true
		key, err := run.ParseChildKey("child")
		if err != nil {
			return run.Transition{}, err
		}
		effect, err := run.NewChildStartEffect(run.ChildSpec{Key: key, ProgramRef: c.definition.child.ProgramRef(), Input: c.state.Input})
		if err != nil {
			return run.Transition{}, err
		}
		effects := []run.Effect{effect}
		if c.definition.duplicate {
			effects = append(effects, effect)
		}
		return run.Yield(consumed, effects)
	}
	for _, message := range turn.Messages {
		switch value := message.(type) {
		case run.Completion:
			if c.state.Retry {
				if value.Outcome.Status() != run.SettlementStatusFailed {
					return run.Transition{}, errors.New("runtest: a reaped child path was reused")
				}
				return run.Complete(consumed, c.state.Input)
			}
			if value.Outcome.Status() == run.SettlementStatusUnknown {
				return run.Transition{}, run.ErrInvalidMessage
			}
			c.state.Completed++
		case run.Exit:
			if value.Termination.Status() != run.StatusCompleted {
				return run.Transition{}, run.ErrInvalidMessage
			}
			c.state.Exited = true
		default:
			return run.Transition{}, run.ErrInvalidMessage
		}
	}
	if c.state.Completed == count && c.state.Exited {
		if c.definition.reuse {
			c.state.Retry = true
			c.state.Started = false
			return run.Continue(consumed)
		}
		return run.Complete(consumed, c.state.Input)
	}
	return run.Yield(consumed, nil)
}

func (c *childExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("runtest.child", c.state)
}
