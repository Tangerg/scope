package agent

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEngineStartRejectsNilContextBeforePublication(t *testing.T) {
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close(context.WithoutCancel(t.Context())) })
	input, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	var nilContext context.Context
	recovered := func() (recovered any) {
		defer func() { recovered = recover() }()
		_, _ = engine.Start(nilContext, newChildTestDeployment(t), input)
		return nil
	}()
	recoveredErr, isError := recovered.(error)
	if !isError || !errors.Is(recoveredErr, errNilContext) {
		t.Fatalf("Start nil-context panic = %v, want %v", recovered, errNilContext)
	}
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if len(engine.processes) != 0 || len(engine.startReservations) != 0 {
		t.Fatalf("nil-context Start published state: processes=%d reservations=%d", len(engine.processes), len(engine.startReservations))
	}
}

func TestProcessRejectsNilContextBeforeControl(t *testing.T) {
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}
	var nilContext context.Context
	for name, call := range map[string]func(){
		"DeliverSignals":       func() { _, _ = process.DeliverSignals(nilContext) },
		"Pause":                func() { _ = process.Pause(nilContext, "pause") },
		"Resume":               func() { _ = process.Resume(nilContext) },
		"RequestCancellation":  func() { _ = process.RequestCancellation(nilContext, "cancel") },
		"Kill":                 func() { _ = process.Kill(nilContext, "kill") },
		"ResolveUnknownEffect": func() { _ = process.ResolveUnknownEffect(nilContext, Settlement{}) },
		"Await":                func() { _, _ = process.Await(nilContext) },
		"Join":                 func() { _ = process.Join(nilContext) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				cause, ok := recover().(error)
				if !ok || !errors.Is(cause, errNilContext) {
					t.Fatalf("nil-context panic = %v, want %v", cause, errNilContext)
				}
			}()
			call()
		})
	}
	if result := awaitResult(t, process); result.Status() != StatusCompleted {
		t.Fatalf("invalid context changed Process outcome to %s", result.Status())
	}
}

func TestResumeRunningProcessReportsInvalidControl(t *testing.T) {
	definition := &signalWindowDefinition{
		engineTestDefinition: newEngineTestDefinition(t, "engine.effect", "effect"),
		entered:              make(chan int, 1), release: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(definition.release) })
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		release()
		mustCloseEngine(t, engine)
	})
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{})
	input, _ := EncodeInput(engineTestInput{Value: "running"})
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	receiveTreeRuntimeProbe(t, definition.entered)
	if err := process.Resume(t.Context()); !errors.Is(err, ErrInvalidProcessControl) {
		t.Errorf("Resume Running Process error=%v, want ErrInvalidProcessControl", err)
	}
	if status := inspectProcessSnapshot(t, process).Status(); status != StatusRunning {
		t.Errorf("rejected Resume changed status to %s", status)
	}
	release()
	if result := awaitResult(t, process); result.Status() != StatusCompleted {
		t.Fatalf("execution after rejected Resume status=%s", result.Status())
	}
}

func TestStepCannotConsumeSignalsThatArriveDuringItsExecution(t *testing.T) {
	for _, consumed := range []uint32{0, 1} {
		name := "preserve later input"
		if consumed == 1 {
			name = "reject unseen consumption"
		}
		t.Run(name, func(t *testing.T) {
			definition := &signalWindowDefinition{
				engineTestDefinition: newEngineTestDefinition(t, "engine.effect", "effect"),
				entered:              make(chan int, 1), release: make(chan struct{}), consumed: consumed,
			}
			engine, err := NewEngine(EngineConfig{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { mustCloseEngine(t, engine) })
			release := sync.OnceFunc(func() { close(definition.release) })
			t.Cleanup(release)
			deployment := engineTestDeployment(t, definition, &engineTestDispatcher{})
			input, _ := EncodeInput(engineTestInput{Value: "original"})
			process, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			if received := <-definition.entered; received != 0 {
				t.Fatalf("first Step received %d Signals, want 0", received)
			}
			id, _ := ParseSignalID("signal:arrived-during-step")
			request, _ := NewSignalRequest(id, WaitID{}, []byte(`{}`))
			if accepted, deliverErr := process.DeliverSignals(t.Context(), request); deliverErr != nil || !accepted {
				t.Fatalf("concurrent input accepted = %t, error = %v", accepted, deliverErr)
			}
			release()
			result := awaitResult(t, process)
			wantStatus := StatusCompleted
			if consumed == 1 {
				wantStatus = StatusFailed
				failure, _ := result.Termination().Failure()
				if failure.Code() != "execution.transition.invalid" {
					t.Fatalf("invalid consumption failure = %+v", failure)
				}
			}
			if result.Status() != wantStatus {
				t.Fatalf("status = %s, want %s", result.Status(), wantStatus)
			}
			snapshot := inspectProcessSnapshot(t, process)
			wire, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			if wire.Mailbox.SignalCursor != 0 || len(wire.Mailbox.Signals) != 1 || wire.Mailbox.Signals[0].ID != id {
				t.Fatalf("later input was consumed: %+v", wire.Mailbox)
			}
			if consumed == 1 && wire.CommittedSteps != 0 {
				t.Fatalf("invalid Step committed %d times", wire.CommittedSteps)
			}
		})
	}
}

type signalWindowDefinition struct {
	*engineTestDefinition
	entered  chan int
	release  chan struct{}
	consumed uint32
}

func (s *signalWindowDefinition) Start(input Input) (Execution, error) {
	execution, err := s.engineTestDefinition.Start(input)
	if err != nil {
		return nil, err
	}
	return &signalWindowExecution{engineTestExecution: execution.(*engineTestExecution), definition: s}, nil
}

func (s *signalWindowDefinition) Restore(state ExecutionState) (Execution, error) {
	execution, err := s.engineTestDefinition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &signalWindowExecution{engineTestExecution: execution.(*engineTestExecution), definition: s}, nil
}

type signalWindowExecution struct {
	*engineTestExecution
	definition *signalWindowDefinition
}

func (s *signalWindowExecution) Step(ctx context.Context, signals []Signal) (Transition, error) {
	s.definition.entered <- len(signals)
	select {
	case <-s.definition.release:
	case <-ctx.Done():
		return Transition{}, ctx.Err()
	}
	s.state.Phase = "done"
	output, err := EncodeOutput(engineTestOutput{Value: s.state.Value})
	if err != nil {
		return Transition{}, err
	}
	return Complete(s.definition.consumed, output)
}

type engineTestInput struct {
	Value string `json:"value"`
}

type engineTestOutput struct {
	Value string `json:"value"`
}

type engineTestState struct {
	Phase  string `json:"phase"`
	Value  string `json:"value"`
	WaitID string `json:"wait_id,omitempty"`
}

type engineTestMessage struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

type engineTestDefinition struct {
	descriptor Descriptor
	mode       string
}

func newEngineTestDefinition(t testing.TB, name, mode string) *engineTestDefinition {
	t.Helper()
	inputSchema, err := SchemaFor[engineTestInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := SchemaFor[engineTestOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: name, Description: "Exercises the Engine lifecycle contract.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &engineTestDefinition{descriptor: descriptor, mode: mode}
}

func (e *engineTestDefinition) Descriptor() Descriptor { return e.descriptor }

func (e *engineTestDefinition) Start(input Input) (Execution, error) {
	value, err := input.Decode[engineTestInput]()
	if err != nil {
		return nil, err
	}
	return &engineTestExecution{mode: e.mode, state: engineTestState{Phase: "ready", Value: value.Value}}, nil
}

func (e *engineTestDefinition) Restore(state ExecutionState) (Execution, error) {
	if state.Kind() != e.descriptor.Name() {
		return nil, ErrInvalidExecutionState
	}
	value, err := wireJSON.decode[engineTestState](state.Payload())
	if err != nil {
		return nil, err
	}
	return &engineTestExecution{mode: e.mode, state: value}, nil
}

type engineTestExecution struct {
	mode  string
	state engineTestState
}

func (e *engineTestExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	switch e.mode {
	case "effect":
		return e.stepEffect(signals)
	case "wait":
		return e.stepWait(signals)
	case "batch":
		return e.stepBatch(signals)
	case "fail":
		e.state.Phase = "corrupted"
		return Transition{}, errors.New("injected Step failure")
	default:
		return Transition{}, errors.New("unknown test mode")
	}
}

func (e *engineTestExecution) stepBatch(signals []Signal) (Transition, error) {
	switch e.state.Phase {
	case "ready":
		e.state.Phase = "batch"
		var effects []Effect
		for _, value := range []string{"first", "second"} {
			payload, _ := json.Marshal(engineTestMessage{Kind: "request", Value: value})
			effect, err := NewDispatcherEffect(payload)
			if err != nil {
				return Transition{}, err
			}
			effects = append(effects, effect)
		}
		return Continue(0, effects...)
	case "batch":
		if len(signals) != 2 {
			return Transition{}, errors.New("batch phase requires two settlement Signals")
		}
		first, err := wireJSON.decode[engineTestMessage](signals[0].Payload())
		if err != nil {
			return Transition{}, err
		}
		second, err := wireJSON.decode[engineTestMessage](signals[1].Payload())
		if err != nil {
			return Transition{}, err
		}
		e.state.Phase = "done"
		output, _ := EncodeOutput(engineTestOutput{Value: first.Value + "+" + second.Value})
		return Complete(2, output)
	default:
		return Transition{}, errors.New("batch execution cannot advance")
	}
}

func (e *engineTestExecution) stepEffect(signals []Signal) (Transition, error) {
	switch e.state.Phase {
	case "ready":
		if len(signals) != 0 {
			return Transition{}, errors.New("ready phase expected no Signal")
		}
		e.state.Phase = "effect"
		payload, _ := json.Marshal(engineTestMessage{Kind: "request", Value: e.state.Value})
		effect, err := NewDispatcherEffect(payload)
		if err != nil {
			return Transition{}, err
		}
		return Continue(0, effect)
	case "effect":
		if len(signals) == 0 {
			return Transition{}, errors.New("effect phase requires settlement Signal")
		}
		message, err := wireJSON.decode[engineTestMessage](signals[0].Payload())
		if err != nil {
			return Transition{}, err
		}
		e.state.Phase = "done"
		output, _ := EncodeOutput(engineTestOutput{Value: message.Value})
		return Complete(1, output)
	default:
		return Transition{}, errors.New("effect execution cannot advance")
	}
}

func (e *engineTestExecution) stepWait(signals []Signal) (Transition, error) {
	switch e.state.Phase {
	case "ready":
		e.state.Phase = "wait_id"
		key, _ := ParseWaitKey("approval")
		payload, _ := json.Marshal(engineTestMessage{Kind: "wait_opened"})
		effect, err := RequestWait(key, payload)
		if err != nil {
			return Transition{}, err
		}
		return Continue(0, effect)
	case "wait_id":
		if len(signals) == 0 {
			return Transition{}, errors.New("wait identity Signal is required")
		}
		waitID, ok := signals[0].WaitID()
		if !ok {
			return Transition{}, errors.New("wait identity Signal has no WaitID")
		}
		e.state.Phase = "answer"
		e.state.WaitID = waitID.String()
		return Wait(1, waitID)
	case "answer":
		if len(signals) == 0 {
			return Transition{}, errors.New("answer Signal is required")
		}
		waitID, _ := signals[0].WaitID()
		if waitID.String() != e.state.WaitID {
			return Transition{}, errors.New("answer addressed another wait")
		}
		message, err := wireJSON.decode[engineTestMessage](signals[0].Payload())
		if err != nil {
			return Transition{}, err
		}
		e.state.Phase = "done"
		output, _ := EncodeOutput(engineTestOutput{Value: message.Value})
		return Complete(1, output)
	default:
		return Transition{}, errors.New("wait execution cannot advance")
	}
}

func (e *engineTestExecution) Snapshot() (ExecutionState, error) {
	payload, err := json.Marshal(e.state)
	if err != nil {
		return ExecutionState{}, err
	}
	name := "engine.effect"
	switch e.mode {
	case "wait":
		name = "engine.wait"
	case "fail":
		name = "engine.fail"
	case "batch":
		name = "engine.batch"
	}
	return NewExecutionState(name, payload)
}

type engineTestDispatcher struct {
	policy  ReplayPolicy
	calls   atomic.Int32
	started chan struct{}
	block   <-chan struct{}
	check   func() error
	deltas  int
}

func (e *engineTestDispatcher) Dispatch(
	_ context.Context,
	request EffectRequest,
	emit DeltaEmitter,
) (Settlement, error) {
	e.calls.Add(1)
	if e.started != nil {
		e.started <- struct{}{}
	}
	if e.check != nil {
		if err := e.check(); err != nil {
			return Settlement{}, err
		}
	}
	if e.block != nil {
		<-e.block
	}
	message, err := wireJSON.decode[engineTestMessage](request.Effect().Payload())
	if err != nil {
		return Settlement{}, err
	}
	delta, _ := json.Marshal(engineTestMessage{Kind: "delta", Value: message.Value})
	count := e.deltas
	if count == 0 {
		count = 1
	}
	for range count {
		emit(delta)
	}
	payload, _ := json.Marshal(engineTestMessage{Kind: "result", Value: message.Value + ":done"})
	return NewSettlement(request.ID(), SettlementStatusSucceeded, payload)
}

func (e *engineTestDispatcher) ReplayPolicy(Effect) ReplayPolicy { return e.policy }

type failingEngineTestDispatcher struct {
	calls atomic.Int32
}

type partialBatchDispatcher struct {
	calls atomic.Int32
}

func (p *partialBatchDispatcher) Dispatch(
	_ context.Context,
	request EffectRequest,
	_ DeltaEmitter,
) (Settlement, error) {
	p.calls.Add(1)
	message, err := wireJSON.decode[engineTestMessage](request.Effect().Payload())
	if err != nil {
		return Settlement{}, err
	}
	if request.BatchIndex() == 1 {
		return Settlement{}, errors.New("second Effect result is unknown")
	}
	payload, _ := json.Marshal(engineTestMessage{Kind: "result", Value: message.Value})
	return NewSettlement(request.ID(), SettlementStatusSucceeded, payload)
}

func (*partialBatchDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }

func (f *failingEngineTestDispatcher) Dispatch(
	context.Context,
	EffectRequest,
	DeltaEmitter,
) (Settlement, error) {
	f.calls.Add(1)
	return Settlement{}, errors.New("external result is unknown")
}

func (*failingEngineTestDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }

func TestEngineRunsEffectToValidatedOutput(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "hello"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() || result.Status() != StatusCompleted || dispatcher.calls.Load() != 1 {
		t.Fatalf("result=%+v calls=%d", result, dispatcher.calls.Load())
	}
	output, ok := result.Output()
	if !ok {
		t.Fatal("completed result has no Output")
	}
	value, err := output.Decode[engineTestOutput]()
	if err != nil || value.Value != "hello:done" {
		t.Fatalf("output=%+v err=%v", value, err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestEngineMintsWaitIDAndRequiresAddressedAnswer(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "question"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusWaiting)
	waitID, ok := inspectProcessSnapshot(t, process).WaitID()
	if !ok {
		t.Fatal("Waiting Process did not expose Engine-minted WaitID")
	}
	plainID, _ := ParseSignalID("signal:plain")
	plain, _ := NewSignalRequest(plainID, WaitID{}, json.RawMessage(`{"kind":"answer","value":"wrong"}`))
	if _, deliverSignalErr := process.DeliverSignals(context.Background(), plain); !errors.Is(deliverSignalErr, ErrSignalRejected) {
		t.Fatalf("unaddressed answer error=%v", deliverSignalErr)
	}
	answerID, _ := ParseSignalID("signal:answer")
	answer, _ := NewSignalRequest(answerID, waitID, json.RawMessage(`{"kind":"answer","value":"approved"}`))
	accepted, err := process.DeliverSignals(context.Background(), answer)
	if err != nil || !accepted {
		t.Fatalf("answer accepted=%t err=%v", accepted, err)
	}
	result := awaitResult(t, process)
	output, _ := result.Output()
	value, _ := output.Decode[engineTestOutput]()
	if value.Value != "approved" {
		t.Fatalf("output=%q", value.Value)
	}
}

func TestEngineCommitsPendingTreeBeforeDispatch(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	durability := &recordingTreeDurability{}
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
	dispatcher.check = func() error {
		if !durability.pending.Load() {
			return errors.New("dispatch happened before pending tree commit")
		}
		return nil
	}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "durable"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	boundaries := durability.effectBoundaries()
	if len(boundaries) < 2 || boundaries[0].Kind() != EffectBoundaryPending ||
		boundaries[1].Kind() != EffectBoundarySettled {
		t.Fatalf("Effect boundaries = %#v", boundaries)
	}
	processSnapshots := boundaries[0].TreeSnapshot().ProcessSnapshots()
	wire, err := processSnapshots[0].wire()
	if err != nil || wire.Prepared == nil ||
		wire.Prepared.Effects[0].Phase != effectPhasePending {
		t.Fatalf("pending boundary does not contain pending Effect: %v", err)
	}
}

func TestUnknownSettlementRequiresExplicitResolutionAndSurvivesRestore(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	dispatcher := &failingEngineTestDispatcher{}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "uncertain"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := waitForUnknownSettlement(t, process)
	wire, _ := snapshot.wire()
	effectID := wire.Prepared.Effects[0].ID
	tree, err := engine.CaptureTree(context.Background(), process.ID())
	if err != nil {
		t.Fatal(err)
	}

	restoredEngine, _ := NewEngine(EngineConfig{})
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	unknown := inspectProcessSnapshot(t, restored).UnknownEffectIDs()
	if len(unknown) != 1 || unknown[0] != effectID {
		t.Fatalf("unknown Effects=%v", unknown)
	}
	if dispatcher.calls.Load() != 1 {
		t.Fatalf("ReplayPolicyNever dispatcher calls=%d, want 1", dispatcher.calls.Load())
	}
	payload, _ := json.Marshal(engineTestMessage{Kind: "result", Value: "resolved"})
	settlement, _ := NewSettlement(effectID, SettlementStatusSucceeded, payload)
	if err := restored.ResolveUnknownEffect(context.Background(), settlement); err != nil {
		t.Fatal(err)
	}
	result := awaitResult(t, restored)
	output, _ := result.Output()
	value, _ := output.Decode[engineTestOutput]()
	if value.Value != "resolved" {
		t.Fatalf("resolved output=%q", value.Value)
	}
	_ = process.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, process)
}

func TestPartialEffectBatchPreservesSettlementsAndDeclarationOrder(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.batch", "batch")
	dispatcher := &partialBatchDispatcher{}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "batch"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := waitForUnknownSettlement(t, process)
	wire, _ := snapshot.wire()
	if len(wire.Prepared.Effects) != 2 || wire.Prepared.Effects[0].Settlement == nil ||
		wire.Prepared.Effects[0].Settlement.Status() != SettlementStatusSucceeded ||
		wire.Prepared.Effects[1].Settlement == nil ||
		wire.Prepared.Effects[1].Settlement.Status() != SettlementStatusUnknown {
		t.Fatalf("prepared batch=%+v", wire.Prepared.Effects)
	}
	tree, err := engine.CaptureTree(context.Background(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	restoredEngine, _ := NewEngine(EngineConfig{})
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	unknown := inspectProcessSnapshot(t, restored).UnknownEffectIDs()
	if len(unknown) != 1 || unknown[0] != wire.Prepared.Effects[1].ID {
		t.Fatalf("unknown Effects=%v", unknown)
	}
	if dispatcher.calls.Load() != 2 {
		t.Fatalf("dispatcher calls=%d, want 2", dispatcher.calls.Load())
	}
	payload, _ := json.Marshal(engineTestMessage{Kind: "result", Value: "second"})
	settlement, _ := NewSettlement(wire.Prepared.Effects[1].ID, SettlementStatusSucceeded, payload)
	if err := restored.ResolveUnknownEffect(context.Background(), settlement); err != nil {
		t.Fatal(err)
	}
	result := awaitResult(t, restored)
	output, _ := result.Output()
	value, _ := output.Decode[engineTestOutput]()
	if value.Value != "first+second" {
		t.Fatalf("ordered batch output=%q", value.Value)
	}
	_ = process.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, process)
}

func TestPausedProcessCapturesRestoresAndResumesAtSafeBoundary(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	release := make(chan struct{})
	dispatcher := &engineTestDispatcher{
		policy:  ReplayPolicySameIdentity,
		started: make(chan struct{}, 1),
		block:   release,
	}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "paused"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	<-dispatcher.started
	if pauseErr := process.Pause(context.Background(), "operator inspection"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	if inspectProcessSnapshot(t, process).Status() == StatusPaused {
		t.Fatal("Pause became visible before the in-flight Effect settled")
	}
	close(release)
	waitForStatus(t, process, StatusPaused)
	tree, err := engine.CaptureTree(context.Background(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	restoredEngine, _ := NewEngine(EngineConfig{})
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	if inspectProcessSnapshot(t, restored).Status() != StatusPaused {
		t.Fatalf("restored status=%s", inspectProcessSnapshot(t, restored).Status())
	}
	if err := restored.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := awaitResult(t, restored)
	if result.Status() != StatusCompleted {
		t.Fatalf("result status=%s", result.Status())
	}
	_ = process.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, process)
}

func TestWaitingProcessRestoresWithSameWaitIdentity(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	limits := Limits{MaxSteps: 3, MaxEffects: 1, MaxSignals: 2, MaxPendingSignals: 2}
	engine, _ := NewEngine(EngineConfig{Limits: limits})
	t.Cleanup(func() { _ = engine.Close(context.WithoutCancel(t.Context())) })
	input, _ := EncodeInput(engineTestInput{Value: "question"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusWaiting)
	waitID, _ := inspectProcessSnapshot(t, process).WaitID()
	tree, err := engine.CaptureTree(context.Background(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	listener := &recordingEventListener{}
	restoredEngine, _ := NewEngine(EngineConfig{
		Limits:         limits,
		EventListeners: []EventListener{listener},
	})
	t.Cleanup(func() { _ = restoredEngine.Close(context.WithoutCancel(t.Context())) })
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	restoredWaitID, ok := inspectProcessSnapshot(t, restored).WaitID()
	if !ok || restoredWaitID != waitID {
		t.Fatalf("restored WaitID=%s want=%s", restoredWaitID, waitID)
	}
	answerID, _ := ParseSignalID("signal:restored-answer")
	answer, _ := NewSignalRequest(answerID, restoredWaitID, json.RawMessage(`{"kind":"answer","value":"restored"}`))
	for _, continued := range []*Process{process, restored} {
		before := inspectProcessSnapshot(t, continued).Usage()
		if accepted, err := continued.DeliverSignals(t.Context(), answer, answer); err != nil || accepted {
			t.Fatalf("duplicate batch accepted=%t error=%v", accepted, err)
		}
		if currentWait, ok := inspectProcessSnapshot(t, continued).WaitID(); !ok || currentWait != waitID || inspectProcessSnapshot(t, continued).Usage() != before {
			t.Fatalf("rejected batch changed wait or usage: wait=%s usage=%+v", currentWait, inspectProcessSnapshot(t, continued).Usage())
		}
		if accepted, err := continued.DeliverSignals(t.Context(), answer); err != nil || !accepted {
			t.Fatalf("accepted=%t err=%v", accepted, err)
		}
		result := awaitResult(t, continued)
		wantUsage := Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}
		output, present := result.Output()
		if result.Status() != StatusCompleted || result.Usage() != wantUsage ||
			!present || string(output.JSON()) != `{"value":"restored"}` {
			t.Fatalf("continued result status=%s usage=%+v output=%s", result.Status(), result.Usage(), output.JSON())
		}
		if _, waiting := inspectProcessSnapshot(t, continued).WaitID(); waiting {
			t.Fatal("completed Process retained its current wait")
		}
	}
	var accepted SignalAcceptedFact
	for _, event := range listener.snapshot() {
		if fact, ok := event.SignalAccepted(); ok {
			accepted = fact
		}
	}
	acceptedWaitID, addressed := accepted.WaitID()
	if !accepted.Valid() || accepted.SignalID() != answerID ||
		!addressed || acceptedWaitID != restoredWaitID {
		t.Fatalf(
			"accepted Signal fact: id=%s wait=%s addressed=%t",
			accepted.SignalID(), acceptedWaitID, addressed,
		)
	}
}

func TestRestoredPreparedEffectReplaysOnlyWithSameIdentityPolicy(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	durability := &recordingTreeDurability{}
	dispatcher := &engineTestDispatcher{policy: ReplayPolicySameIdentity}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, _ := NewEngine(EngineConfig{TreeDurability: durability})
	input, _ := EncodeInput(engineTestInput{Value: "replay"})
	if _, err := engine.Run(context.Background(), deployment, input); err != nil {
		t.Fatal(err)
	}
	snapshot := durability.effectBoundaries()[0].TreeSnapshot().ProcessSnapshots()[0]
	tree := singleProcessTreeSnapshot(t, snapshot)
	restoredEngine, _ := NewEngine(EngineConfig{})
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	if result := awaitResult(t, restored); result.Status() != StatusCompleted {
		t.Fatalf("result status=%s", result.Status())
	}
	if dispatcher.calls.Load() != 2 {
		t.Fatalf("same-identity dispatcher calls=%d, want 2", dispatcher.calls.Load())
	}
}

func TestRestoreDistinguishesPlannedFromPendingEffect(t *testing.T) {
	prepared := preparedEngineTestSnapshot(t)
	definition := newEngineTestDefinition(t, "engine.effect", "effect")

	t.Run("planned dispatches even under never-replay policy", func(t *testing.T) {
		dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
		deployment := engineTestDeployment(t, definition, dispatcher)
		engine, err := NewEngine(EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := engine.RestoreTree(
			context.Background(), deployment, singleProcessTreeSnapshot(t, prepared),
		)
		if err != nil {
			t.Fatal(err)
		}
		if result := awaitResult(t, restored); result.Status() != StatusCompleted {
			t.Fatalf("result status = %s", result.Status())
		}
		if calls := dispatcher.calls.Load(); calls != 1 {
			t.Fatalf("planned Effect dispatch calls = %d, want 1", calls)
		}
		if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("pending becomes unknown under never-replay policy", func(t *testing.T) {
		wire, err := prepared.wire()
		if err != nil {
			t.Fatal(err)
		}
		if beginErr := wire.Prepared.Effects[0].begin(); beginErr != nil {
			t.Fatal(beginErr)
		}
		pending, err := newProcessSnapshot(wire)
		if err != nil {
			t.Fatal(err)
		}
		dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
		deployment := engineTestDeployment(t, definition, dispatcher)
		engine, err := NewEngine(EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := engine.RestoreTree(
			context.Background(), deployment, singleProcessTreeSnapshot(t, pending),
		)
		if err != nil {
			t.Fatal(err)
		}
		unknown := inspectProcessSnapshot(t, restored).UnknownEffectIDs()
		if len(unknown) != 1 {
			t.Fatalf("unknown Effects = %v", unknown)
		}
		if calls := dispatcher.calls.Load(); calls != 0 {
			t.Fatalf("pending never-replay dispatch calls = %d, want 0", calls)
		}
		if err := restored.Kill(context.Background(), "test cleanup"); err != nil {
			t.Fatal(err)
		}
		_ = awaitResult(t, restored)
		if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStartContextCancellationMapsToHostCancellation(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, _ := NewEngine(EngineConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	input, _ := EncodeInput(engineTestInput{Value: "cancel"})
	process, err := engine.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusWaiting)
	cancel()
	result := awaitResult(t, process)
	if result.Status() != StatusCanceled || result.Termination().Cause() != TerminationCauseHostCancellation {
		t.Fatalf("termination=%+v", result.Termination())
	}
}

func TestRequestCancellationReturnsAfterSubmissionAndSurvivesContextCancellation(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	release := make(chan struct{})
	dispatcher := &engineTestDispatcher{
		policy:  ReplayPolicyNever,
		started: make(chan struct{}, 1),
		block:   release,
	}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "cancel after submission"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	<-dispatcher.started

	requestCtx, cancelRequest := context.WithTimeout(context.Background(), time.Second)
	if err := process.RequestCancellation(requestCtx, "operator canceled the Process"); err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	if inspectProcessSnapshot(t, process).Status().Terminal() {
		t.Fatal("cancellation became terminal before the in-flight Effect settled")
	}

	close(release)
	result := awaitResult(t, process)
	if result.Status() != StatusCanceled || result.Termination().Cause() != TerminationCauseHostCancellation {
		t.Fatalf("termination=%+v", result.Termination())
	}
}

func TestRequestCancellationRejectsAnAlreadyCanceledSubmissionContext(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "remain waiting"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusWaiting)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if err := process.RequestCancellation(requestCtx, "must not enter the queue"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RequestCancellation error=%v, want context.Canceled", err)
	}
	if inspectProcessSnapshot(t, process).Status() != StatusWaiting {
		t.Fatalf("status=%s, want waiting", inspectProcessSnapshot(t, process).Status())
	}

	if err := process.Kill(context.Background(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	_ = awaitResult(t, process)
}

func TestKillWaitsForInflightEffectSettlement(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	release := make(chan struct{})
	dispatcher := &engineTestDispatcher{
		policy:  ReplayPolicyNever,
		started: make(chan struct{}, 1),
		block:   release,
	}
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "slow"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	<-dispatcher.started
	if err := process.Kill(context.Background(), "operator requested stop"); err != nil {
		t.Fatal(err)
	}
	if inspectProcessSnapshot(t, process).Status().Terminal() {
		t.Fatal("Kill abandoned an in-flight Effect")
	}
	close(release)
	result := awaitResult(t, process)
	if result.Status() != StatusKilled || result.Termination().Cause() != TerminationCauseEngineKill {
		t.Fatalf("termination=%+v", result.Termination())
	}
}

func TestStepFailureDiscardsMutatedExecutionAndPreservesCursor(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.fail", "fail")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	listener := &recordingEventListener{}
	engine, _ := NewEngine(EngineConfig{EventListeners: []EventListener{listener}})
	input, _ := EncodeInput(engineTestInput{Value: "stable"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	result := awaitResult(t, process)
	if result.Status() != StatusFailed || result.Termination().Cause() != TerminationCauseExecutionFailure {
		t.Fatalf("termination=%+v", result.Termination())
	}
	snapshot := inspectProcessSnapshot(t, process)
	wire, _ := snapshot.wire()
	state, _ := wireJSON.decode[engineTestState](wire.CommittedExecutionState.Payload())
	if state.Phase != "ready" || wire.Mailbox.SignalCursor != 0 || wire.Prepared != nil {
		t.Fatalf("committed execution state=%+v cursor=%d prepared=%v", state, wire.Mailbox.SignalCursor, wire.Prepared)
	}
	var finished ProcessFinishedFact
	for _, event := range listener.snapshot() {
		if event.Name() == EventProcessFinished {
			var ok bool
			finished, ok = event.ProcessFinished()
			if !ok {
				t.Fatalf("finished event payload = %s", event.Payload())
			}
		}
	}
	kind, code, failed := finished.Failure()
	if !failed || kind != FailureKindExecution || code != "execution.step.failed" {
		t.Fatalf("finished failure = %s/%q present=%t", kind, code, failed)
	}
	if finished.Usage() != result.Usage() {
		t.Fatalf("finished usage = %+v, result usage = %+v", finished.Usage(), result.Usage())
	}
}

func TestEngineEnforcesStepLimitAndReportsMonotonicUsage(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, err := NewEngine(EngineConfig{Limits: Limits{
		MaxSteps: 1, MaxEffects: 1, MaxSignals: 1, MaxPendingSignals: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "bounded"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != StatusFailed {
		t.Fatalf("status=%s", result.Status())
	}
	failure, ok := result.Termination().Failure()
	if !ok || failure.Code() != "engine.limit.steps" {
		t.Fatalf("failure=%+v", failure)
	}
	usage := result.Usage()
	if usage.CommittedSteps != 1 || usage.PreparedEffects != 1 || usage.AcceptedSignals != 1 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestEngineReportsInvalidLimitRelation(t *testing.T) {
	_, err := NewEngine(EngineConfig{Limits: Limits{MaxSignals: 1, MaxPendingSignals: 2}})
	if !errors.Is(err, ErrInvalidEngineConfig) ||
		!strings.Contains(err.Error(), "MaxPendingSignals (2) exceeds MaxSignals (1)") {
		t.Fatalf("limit error = %v", err)
	}

	_, err = NewEngine(EngineConfig{TreeLimits: TreeLimits{MaxChildren: 1, MaxActiveChildren: 2}})
	if !errors.Is(err, ErrInvalidEngineConfig) ||
		!strings.Contains(err.Error(), "MaxActiveChildren (2) exceeds MaxChildren (1)") {
		t.Fatalf("tree limit error = %v", err)
	}
}

type recordingEventListener struct {
	mu     sync.Mutex
	events []Event
	panic  bool
}

func (r *recordingEventListener) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func (r *recordingEventListener) OnEvent(_ context.Context, event Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	shouldPanic := r.panic
	r.mu.Unlock()
	if shouldPanic {
		panic("observer failure")
	}
}

type blockingDeltaListener struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingDeltaListener) OnDelta(context.Context, Delta) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
}

func TestDeltaBufferDropsAreObservableAndListenerPanicIsIsolated(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever, deltas: 100}
	deployment := engineTestDeployment(t, definition, dispatcher)
	events := &recordingEventListener{panic: true}
	deltas := &blockingDeltaListener{entered: make(chan struct{}), release: make(chan struct{})}
	engine, err := NewEngine(EngineConfig{
		EventListeners: []EventListener{events}, DeltaListeners: []DeltaListener{deltas}, DeltaBufferCapacity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "stream"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var dropped DeltaDroppedFact
	for _, event := range events.snapshot() {
		if fact, ok := event.DeltaDropped(); ok {
			dropped = fact
		}
	}
	if result.Usage().DroppedDeltas == 0 || !dropped.Valid() ||
		dropped.Count() != result.Usage().DroppedDeltas {
		t.Fatalf("usage=%+v dropped fact=%d", result.Usage(), dropped.Count())
	}
	close(deltas.release)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestFlushDeltasWaitsForAcceptedListenerDelivery(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever, deltas: 2}
	deployment := engineTestDeployment(t, definition, dispatcher)
	deltas := &blockingDeltaListener{entered: make(chan struct{}), release: make(chan struct{})}
	engine, err := NewEngine(EngineConfig{
		DeltaListeners: []DeltaListener{deltas}, DeltaBufferCapacity: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "stream"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	<-deltas.entered
	flushed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		flushed <- engine.FlushDeltas(ctx)
	}()
	select {
	case err := <-flushed:
		t.Fatalf("FlushDeltas returned before the listener: %v", err)
	default:
	}
	close(deltas.release)
	if err := <-flushed; err != nil {
		t.Fatalf("FlushDeltas: %v", err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestEventLifecycleCarriesExactBindingAndAttemptDurations(t *testing.T) {
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	listener := &recordingEventListener{}
	engine, err := NewEngine(EngineConfig{EventListeners: []EventListener{listener}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "events"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	events := listener.snapshot()
	wantNames := []string{
		EventProcessStarted,
		EventStepStarted, EventStepFinished, EventStepPrepared,
		EventEffectStarted, EventEffectFinished, EventStepCommitted,
		EventStepStarted, EventStepFinished, EventStepPrepared, EventStepCommitted,
		EventProcessFinished,
	}
	assertEventSequence(t, events, wantNames)
	assertEventEnvelopes(t, events, result, deployment)
	assertEventPayloads(t, events, result)
}

func assertEventSequence(t *testing.T, events []Event, wantNames []string) {
	t.Helper()
	if len(events) != len(wantNames) {
		t.Fatalf("events = %d, want %d: %#v", len(events), len(wantNames), eventNames(events))
	}
	for index, event := range events {
		if event.Name() != wantNames[index] {
			t.Fatalf("event[%d] = %q, want %q", index, event.Name(), wantNames[index])
		}
	}
}

func assertEventEnvelopes(t *testing.T, events []Event, result Result, deployment Deployment) {
	t.Helper()
	for index, event := range events {
		if event.ProcessSequence() != uint64(index+1) || event.ProcessID() != result.ProcessID() ||
			event.DeploymentRef() != deployment.DeploymentRef() ||
			event.Relation().ProcessID() != result.ProcessID() || !event.Relation().IsRoot() {
			t.Fatalf("event[%d] envelope = %+v", index, event)
		}
	}
}

func assertEventPayloads(t *testing.T, events []Event, result Result) {
	t.Helper()
	for _, event := range events {
		if !validEventPayload(event, result) {
			t.Fatalf("event %q payload = %s", event.Name(), event.Payload())
		}
	}
}

func validEventPayload(event Event, result Result) bool {
	switch event.Name() {
	case EventEffectStarted:
		fact, ok := event.EffectStarted()
		return ok && fact.Target() == EffectTargetDispatcher
	case EventStepFinished:
		fact, ok := event.StepFinished()
		return ok && fact.Duration() >= 0 && fact.Status() == StepStatusSucceeded
	case EventEffectFinished:
		fact, ok := event.EffectFinished()
		return ok && fact.Duration() >= 0 && fact.SettlementStatus() == SettlementStatusSucceeded &&
			fact.Target() == EffectTargetDispatcher
	case EventStepCommitted:
		fact, ok := event.StepCommitted()
		return ok && fact.Valid()
	case EventProcessFinished:
		fact, ok := event.ProcessFinished()
		return ok && fact.Status() == StatusCompleted &&
			fact.Cause() == TerminationCauseCompletion && fact.Usage() == result.Usage()
	default:
		return true
	}
}

func TestFrameworkEffectPublishesTheSameLifecycleContract(t *testing.T) {
	deployment := newChildTestDeployment(t)
	listener := &recordingEventListener{}
	engine, err := NewEngine(EngineConfig{EventListeners: []EventListener{listener}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "parent"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, root))
	if len(output.ChildIDs) != 1 {
		t.Fatalf("root output = %#v", output)
	}
	childID, _ := ParseProcessID(output.ChildIDs[0])
	child, _ := engine.Process(childID)
	_ = mustAwait(t, child)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	var started, finished bool
	for _, event := range listener.snapshot() {
		if event.ProcessID() != root.ID() ||
			(event.Name() != EventEffectStarted && event.Name() != EventEffectFinished) {
			continue
		}
		switch event.Name() {
		case EventEffectStarted:
			fact, ok := event.EffectStarted()
			started = ok && fact.Target() == EffectTargetFramework
		case EventEffectFinished:
			fact, ok := event.EffectFinished()
			finished = ok && fact.Target() == EffectTargetFramework
		}
	}
	if !started || !finished {
		t.Fatalf("Framework Effect lifecycle started=%t finished=%t", started, finished)
	}
}

func eventNames(events []Event) []string {
	names := make([]string, len(events))
	for index, event := range events {
		names[index] = event.Name()
	}
	return names
}

func engineTestDeployment(t testing.TB, definition Definition, dispatcher Dispatcher) Deployment {
	t.Helper()
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: ComputeDigest([]byte(definition.Descriptor().Name() + ":implementation")),
		ConfigurationDigest:  ComputeDigest([]byte(definition.Descriptor().Name() + ":configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func awaitResult(t *testing.T, process *Process) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := process.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func waitForStatus(t testing.TB, process *Process, want Status) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		snapshot := inspectProcessSnapshot(t, process)
		if snapshot.Status() == want {
			return
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("Process status=%s, want %s: %v", snapshot.Status(), want, err)
		}
		runtime.Gosched()
	}
}

func waitForUnknownSettlement(t testing.TB, process *Process) ProcessSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		snapshot := inspectProcessSnapshot(t, process)
		if len(snapshot.UnknownEffectIDs()) != 0 {
			return snapshot
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("Process did not expose an Unknown settlement: %v", err)
		}
		runtime.Gosched()
	}
}

func singleProcessTreeSnapshot(t *testing.T, snapshot ProcessSnapshot) TreeSnapshot {
	t.Helper()
	tree, err := newTreeSnapshot(treeSnapshotWire{
		RootID:           snapshot.ProcessID(),
		ProcessSnapshots: []ProcessSnapshot{snapshot},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestCanceledControlDoesNotEnterTheRuntime(t *testing.T) {
	release := make(chan struct{})
	dispatcher := &engineTestDispatcher{
		policy: ReplayPolicyNever, started: make(chan struct{}, 1), block: release,
	}
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "complete normally"})
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	<-dispatcher.started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 64 {
		if err := process.Kill(ctx, "must not be admitted"); !errors.Is(err, context.Canceled) {
			t.Errorf("Kill error=%v, want context.Canceled", err)
		}
	}
	close(release)
	result := awaitResult(t, process)
	if result.Status() != StatusCompleted {
		t.Errorf("already canceled control changed result to %s", result.Status())
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}
