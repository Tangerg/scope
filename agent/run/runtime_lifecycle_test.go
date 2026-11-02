package run

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func lifecycleEchoProgram(t testing.TB, runtime *Runtime, name string) Program[int, int] {
	t.Helper()
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, name), step: func(_ context.Context, turn Turn) (Transition, error) {
		if len(turn.Messages) == 0 {
			return Yield(0, nil, kernelWait())
		}
		signal, ok := turn.Messages[0].(Signal)
		if !ok {
			return Transition{}, ErrInvalidMessage
		}
		return Complete(1, signal.Payload)
	}}
	return controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
}

func lifecycleCallProgram(t testing.TB, runtime *Runtime, dispatcher Dispatcher) Program[int, int] {
	t.Helper()
	effect := controlValue(NewDispatcherEffect([]byte(`0`)))
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.lifecycle.call"), step: func(_ context.Context, turn Turn) (Transition, error) {
		if turn.Step == 1 {
			return Yield(0, []Effect{effect})
		}
		return Complete(uint32(len(turn.Messages)), controlValue(EncodePayload(7)))
	}}
	return controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
}

func lifecycleUnknown(t *testing.T, ctx context.Context, handle *Handle[int]) EffectID {
	t.Helper()
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if settled, ok := entry.Record.(EffectSettled); ok && settled.Outcome.Status() == SettlementStatusUnknown {
			return settled.ID
		}
	}
	t.Fatal("effect never acquired an unknown outcome")
	return EffectID{}
}

func TestKillRetainsUncertaintyAcrossRecoveryUntilReconciled(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	var calls atomic.Int32
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicySameIdentity}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
		calls.Add(1)
		return NewSettlement(SettlementStatusUnknown, []byte(`null`))
	}}
	program := lifecycleCallProgram(t, runtime, dispatcher)
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	effect := lifecycleUnknown(t, ctx, handle)
	if err := handle.Cancel(ctx, "stop requested"); err != nil {
		t.Fatal(err)
	}
	if err := handle.Kill(ctx, "kill requested"); err != nil {
		t.Fatal(err)
	}
	inspection := controlValue(handle.Inspect(ctx))
	process, found := inspection.Process(handle.ID())
	if !found || process.Status != StatusDraining || len(process.PendingEffects) != 1 {
		t.Fatalf("kill discarded unresolved work: %+v", process)
	}
	if err := handle.ReplayUnknownEffect(ctx, effect); !errors.Is(err, ErrEffectReplayForbidden) {
		t.Fatalf("killed process replayed external work: %v", err)
	}
	if err := handle.ResolveUnknownEffect(ctx, effect, controlValue(NewSettlement(SettlementStatusUnknown, []byte(`null`)))); !errors.Is(err, ErrEffectNotPending) {
		t.Fatalf("uncertainty resolved itself: %v", err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, stopped := handle.Await(ctx)
	var stoppedRuntime *RuntimeError
	if !errors.As(stopped, &stoppedRuntime) || !errors.Is(stopped, ErrClosed) || len(stoppedRuntime.UnresolvedEffectIDs()) != 1 {
		t.Fatalf("close hid retained uncertainty: %v", stopped)
	}
	replacement := kernelRuntime(t, Config{Store: store})
	recovered := controlValue(replacement.Recover(ctx, lifecycleCallProgram(t, replacement, dispatcher), handle.ID()))
	if recovered.Epoch() != handle.Epoch()+1 || calls.Load() != 1 {
		t.Fatal("recovery changed the pending effect identity or replayed killed work")
	}
	if err := recovered.ResolveUnknownEffect(ctx, effect, controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`7`)))); err != nil {
		t.Fatal(err)
	}
	result := controlValue(recovered.Await(ctx))
	if result.Termination().Status() != StatusKilled || result.Termination().Reason() != "kill requested" || calls.Load() != 1 {
		t.Fatalf("reconciliation changed the terminal cause: %+v", result.Termination())
	}
	log, _, err := store.Load(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	var settledAt, exitedAt LSN
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case EffectSettled:
			if record.Outcome.Status() != SettlementStatusUnknown {
				settledAt = entry.LSN
			}
		case Exited:
			exitedAt = entry.LSN
		}
	}
	if settledAt == 0 || exitedAt <= settledAt {
		t.Fatal("Exit preceded reconciliation")
	}
}

func TestForegroundContextControlsTheTreeButStartDetaches(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		wantErr, wantCause := error(context.Canceled), TerminationCauseHostCancellation
		if deadline {
			name, wantErr, wantCause = "deadline", context.DeadlineExceeded, TerminationCauseHostDeadline
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan ProcessID, 1)
			runtime := kernelRuntime(t, Config{})
			definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.foreground"), step: func(_ context.Context, turn Turn) (Transition, error) {
				if turn.Step == 1 {
					started <- turn.Process
				}
				return Yield(uint32(len(turn.Messages)), nil, kernelWait())
			}}
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 300*time.Millisecond)
			}
			defer cancel()
			returned := make(chan error, 1)
			go func() {
				_, err := runtime.Run(ctx, program, 0)
				returned <- err
			}()
			observation, observationCancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer observationCancel()
			var root ProcessID
			select {
			case root = <-started:
			case <-observation.Done():
				t.Fatal(observation.Err())
			}
			observer := &Handle[int]{runtime: runtime, id: root, program: program.ProgramRef(), epoch: 1}
			waitForKernelStep(t, observation, observer, 1)
			if !deadline {
				cancel()
			}
			select {
			case err := <-returned:
				if !errors.Is(err, wantErr) {
					t.Fatalf("foreground context outcome: %v", err)
				}
			case <-observation.Done():
				t.Fatal("foreground execution ignored caller cancellation")
			}
			result := controlValue(observer.Await(observation))
			if result.Termination().Cause() != wantCause {
				t.Fatalf("foreground context cause=%s", result.Termination().Cause())
			}
		})
		t.Run("detached "+name, func(t *testing.T) {
			runtime := kernelRuntime(t, Config{})
			program := lifecycleEchoProgram(t, runtime, "test.detached")
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
			}
			defer cancel()
			handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
			observation, observationCancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer observationCancel()
			waitForKernelStep(t, observation, handle, 1)
			if !deadline {
				cancel()
			}
			<-ctx.Done()
			if err := handle.Answer(observation, kernelWait().Key, 42); err != nil {
				t.Fatal(err)
			}
			result := controlValue(handle.Await(observation))
			if output, err := result.Output(); err != nil || output != 42 {
				t.Fatalf("Start retained caller cancellation: output=%d error=%v", output, err)
			}
		})
	}
}

func TestReleaseTreeDropsOnlyStoppedLocalOwnership(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	program := lifecycleEchoProgram(t, runtime, "test.release")
	request := StartRequest[int]{ID: hostStartID()}
	handle := controlValue(runtime.Start(t.Context(), program, request))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, handle, 1)
	if err := runtime.ReleaseTree(ctx, handle.ID()); !errors.Is(err, ErrActiveProcesses) {
		t.Fatalf("released an active tree: %v", err)
	}
	if _, err := runtime.Recover(ctx, program, handle.ID()); !errors.Is(err, ErrActiveProcesses) {
		t.Fatalf("recovered over an active local writer: %v", err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReleaseTree(ctx, handle.ID()); err != nil {
		t.Fatal(err)
	}
	if _, present := runtime.Process(handle.ID()); present {
		t.Fatal("released tree retains a process authority")
	}
	if err := runtime.ReleaseTree(ctx, handle.ID()); !errors.Is(err, ErrTreeNotFound) {
		t.Fatalf("duplicate release=%v", err)
	}
	if _, err := runtime.Start(ctx, program, request); !errors.Is(err, ErrClosed) {
		t.Fatalf("released closed runtime restarted work: %v", err)
	}
	if _, err := runtime.Inspect(ctx, handle.ID()); err != nil {
		t.Fatalf("release erased durable observations: %v", err)
	}
	replacement := kernelRuntime(t, Config{Store: store})
	replacementProgram := lifecycleEchoProgram(t, replacement, "test.release")
	recovered := controlValue(replacement.Recover(ctx, replacementProgram, handle.ID()))
	if err := recovered.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	result := controlValue(recovered.Await(ctx))
	if output, err := result.Output(); err != nil || output != 42 {
		t.Fatalf("released work did not recover: %d %v", output, err)
	}
	select {
	case <-recovered.tree.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := replacement.ReleaseTree(ctx, handle.ID()); err != nil {
		t.Fatal(err)
	}
	observer := controlValue(replacement.Start(ctx, replacementProgram, request))
	if observer.tree != nil || observer.Epoch() != recovered.Epoch() {
		t.Fatal("retry recreated local ownership after release")
	}
	if output, err := controlValue(observer.Await(ctx)).Output(); err != nil || output != 42 {
		t.Fatalf("released result disappeared: %d %v", output, err)
	}
}

func TestConcurrentDuplicateStartSharesOneAdmissionAndEpoch(t *testing.T) {
	store := NewMemoryStore()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var admissions atomic.Int32
	runtime := kernelRuntime(t, Config{Store: store, ProcessAdmitter: ProcessAdmitterFunc(func(ctx context.Context, _ ProcessAdmission) error {
		if admissions.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})})
	program := lifecycleEchoProgram(t, runtime, "test.concurrent_start")
	request := StartRequest[int]{ID: hostStartID(), Input: 7}
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	type startResult struct {
		handle *Handle[int]
		err    error
	}
	const callers = 8
	results := make(chan startResult, callers)
	start := func() {
		handle, err := runtime.Start(ctx, program, request)
		results <- startResult{handle, err}
	}
	go start()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for index := 1; index < callers; index++ {
		go start()
	}
	canceled, cancelDuplicate := context.WithCancel(ctx)
	failed := make(chan error, 1)
	go func() {
		_, err := runtime.Start(canceled, program, request)
		failed <- err
	}()
	cancelDuplicate()
	if err := <-failed; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled duplicate affected the active claim: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	var first *Handle[int]
	for range callers {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if first == nil {
				first = result.handle
			}
			if result.handle.ID() != first.ID() || result.handle.Epoch() != first.Epoch() || result.handle.tree != first.tree {
				t.Fatal("duplicate start acquired another execution authority")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	log, _, err := store.Load(ctx, first.ID())
	if err != nil {
		t.Fatal(err)
	}
	starts, activations := 0, 0
	for _, entry := range log.Entries {
		switch entry.Record.(type) {
		case Started:
			starts++
		case Activated:
			activations++
		}
	}
	if admissions.Load() != 1 || starts != 1 || activations != 1 {
		t.Fatalf("duplicate durable facts: admissions=%d starts=%d epochs=%d", admissions.Load(), starts, activations)
	}
	if err := first.Cancel(ctx, "test finished"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Await(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalSignalRetriesUseTheCommittedReceipt(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	program := lifecycleEchoProgram(t, runtime, "test.terminal_receipt")
	request := StartRequest[int]{ID: hostStartID()}
	handle := controlValue(runtime.Start(t.Context(), program, request))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, handle, 1)
	if err := handle.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	controlValue(handle.Await(ctx))
	<-handle.tree.done
	signal := answerSignal(kernelWait().Key, controlValue(EncodePayload(42)))
	if accepted, err := handle.DeliverSignals(ctx, signal); err != nil || accepted {
		t.Fatalf("acknowledged signal could not be retried after Exit: accepted=%t error=%v", accepted, err)
	}
	if err := handle.Answer(ctx, kernelWait().Key, 43); !errors.Is(err, ErrSignalConflict) {
		t.Fatalf("changed terminal answer was accepted: %v", err)
	}
	before := controlValue(handle.Inspect(ctx)).Head
	fresh := Signal{ID: controlValue(ParseSignalID("new-after-exit")), Payload: controlValue(EncodePayload(1))}
	if accepted, err := handle.DeliverSignals(ctx, signal, fresh); err == nil || accepted {
		t.Fatalf("partial terminal batch admitted a new signal: accepted=%t error=%v", accepted, err)
	}
	if after := controlValue(handle.Inspect(ctx)).Head; after != before {
		t.Fatal("terminal retry advanced the durable log")
	}
	if err := runtime.ReleaseTree(ctx, handle.ID()); err != nil {
		t.Fatal(err)
	}
	observer := controlValue(runtime.Start(ctx, program, request))
	if err := observer.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatalf("receipt disappeared with local actor: %v", err)
	}
}

func TestReapedChildAnswerRetryUsesHistoricalReceipt(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	child := lifecycleEchoProgram(t, runtime, "test.child_receipt")
	parent := &treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.parent_receipt"), children: []Program[Payload, Payload]{child.Erase()}, hold: true}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: parent, Revision: "one"}))
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	childID := controlValue(handle.ID().Child(controlValue(ParseChildKey("child-0"))))
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if stepped, ok := entry.Record.(Stepped); ok && stepped.Process == childID {
			break
		}
	}
	childHandle, found := runtime.Process(childID)
	if !found {
		t.Fatal("admitted child not locally available")
	}
	if err := childHandle.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if reaped, ok := entry.Record.(Reaped); ok && reaped.Process == childID {
			break
		}
	}
	if _, found := runtime.Process(childID); found {
		t.Fatal("drained child still owns active process state")
	}
	if err := childHandle.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatalf("reaped child lost acknowledged answer receipt: %v", err)
	}
	if err := childHandle.Answer(ctx, kernelWait().Key, 43); !errors.Is(err, ErrSignalConflict) {
		t.Fatalf("changed answer after Reaped=%v", err)
	}
	if err := handle.Cancel(ctx, "test finished"); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Await(ctx); err != nil {
		t.Fatal(err)
	}
}

type lifecycleFaultStore struct {
	store       Store
	kind        RecordKind
	cause       error
	commitFirst bool
	failed      atomic.Bool
}

func (l *lifecycleFaultStore) Load(ctx context.Context, root ProcessID) (Log, bool, error) {
	return l.store.Load(ctx, root)
}

func (l *lifecycleFaultStore) Read(ctx context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
	return l.store.Read(ctx, root, after)
}

func (l *lifecycleFaultStore) Append(ctx context.Context, batch Batch) error {
	for _, record := range batch.Records {
		if record.recordKind() != l.kind || !l.failed.CompareAndSwap(false, true) {
			continue
		}
		if l.commitFirst {
			if err := l.store.Append(ctx, batch); err != nil {
				return err
			}
		}
		return l.cause
	}
	return l.store.Append(ctx, batch)
}

func TestRuntimeErrorPreservesStoreCauseAndAcknowledgedCoordinates(t *testing.T) {
	for _, commitFirst := range []bool{false, true} {
		name := "before commit"
		if commitFirst {
			name = "lost acknowledgment"
		}
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			cause := errors.New("storage transport interrupted")
			fault := &lifecycleFaultStore{store: store, kind: RecordKindEffectSettled, cause: cause, commitFirst: commitFirst}
			runtime := kernelRuntime(t, Config{Store: fault})
			var calls atomic.Int32
			dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
				calls.Add(1)
				return NewSettlement(SettlementStatusSucceeded, []byte(`7`))
			}}
			program := lifecycleCallProgram(t, runtime, dispatcher)
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			_, err := handle.Await(ctx)
			var runtimeErr *RuntimeError
			if !errors.Is(err, cause) || !errors.As(err, &runtimeErr) {
				t.Fatalf("runtime lost underlying storage failure: %v", err)
			}
			pending := runtimeErr.UnresolvedEffectIDs()
			if runtimeErr.ProcessID() != handle.ID() || runtimeErr.Epoch() != handle.Epoch() || len(pending) != 1 || pending[0].Process != handle.ID() || !strings.Contains(runtimeErr.Error(), handle.ID().String()) {
				t.Fatalf("runtime failure has misleading coordinates: %+v", runtimeErr)
			}
			pending[0] = EffectID{}
			if !runtimeErr.UnresolvedEffectIDs()[0].Valid() {
				t.Fatal("caller mutation changed unresolved diagnostic identities")
			}
			log, _, loadErr := store.Load(ctx, handle.ID())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			wantHead := runtimeErr.After()
			if commitFirst {
				wantHead++
			}
			if log.Head() != wantHead {
				t.Fatalf("acknowledged head=%d durable head=%d", runtimeErr.After(), log.Head())
			}
			replacement := kernelRuntime(t, Config{Store: store})
			recovered := controlValue(replacement.Recover(ctx, lifecycleCallProgram(t, replacement, dispatcher), handle.ID()))
			if !commitFirst {
				id := lifecycleUnknown(t, ctx, recovered)
				if err := recovered.ResolveUnknownEffect(ctx, id, controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`7`)))); err != nil {
					t.Fatal(err)
				}
			}
			result := controlValue(recovered.Await(ctx))
			if output, outputErr := result.Output(); outputErr != nil || output != 7 || calls.Load() != 1 {
				t.Fatalf("storage recovery repeated external work: output=%d calls=%d err=%v", output, calls.Load(), outputErr)
			}
		})
	}
}

func TestFailedSignalAdmissionPreservesItsStoreCauseOnRetry(t *testing.T) {
	store := NewMemoryStore()
	cause := errors.New("signal receipt was not committed")
	fault := &lifecycleFaultStore{store: store, kind: RecordKindSignalAdmitted, cause: cause}
	runtime := kernelRuntime(t, Config{Store: fault})
	program := lifecycleEchoProgram(t, runtime, "test.failed_signal")
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, handle, 1)
	before := controlValue(handle.Inspect(ctx)).Head
	if err := handle.Answer(ctx, kernelWait().Key, 42); !errors.Is(err, cause) {
		t.Fatalf("failed acknowledgment lost its storage cause: %v", err)
	}
	select {
	case <-handle.tree.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var runtimeErr *RuntimeError
	if err := handle.Answer(ctx, kernelWait().Key, 42); !errors.Is(err, cause) || !errors.Is(err, ErrProcessNotActive) || !errors.As(err, &runtimeErr) {
		t.Fatalf("stopped writer retry hid its admission failure: %v", err)
	}
	if after := controlValue(handle.Inspect(ctx)).Head; after != before || runtimeErr.After() != before {
		t.Fatalf("failed admission advanced durable history: before=%d after=%d diagnostic=%d", before, after, runtimeErr.After())
	}
	replacement := kernelRuntime(t, Config{Store: store})
	recovered := controlValue(replacement.Recover(ctx, lifecycleEchoProgram(t, replacement, "test.failed_signal"), handle.ID()))
	if err := recovered.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	result := controlValue(recovered.Await(ctx))
	if output, err := result.Output(); err != nil || output != 42 || result.Usage().AcceptedMessages != 1 {
		t.Fatalf("recovery treated a failed admission as committed: output=%d usage=%+v error=%v", output, result.Usage(), err)
	}
}

func TestRejectedSignalDoesNotClaimAcceptanceWhileTheWriterDrains(t *testing.T) {
	cause := errors.New("signal append rejected")
	store := &lifecycleFaultStore{store: NewMemoryStore(), kind: RecordKindSignalAdmitted, cause: cause}
	runtime := kernelRuntime(t, Config{Store: store})
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	defer released.Do(func() { close(release) })
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
		close(entered)
		<-release
		return NewSettlement(SettlementStatusSucceeded, []byte(`0`))
	}}
	effect := controlValue(NewDispatcherEffect([]byte(`0`)))
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.admission_failure"), step: func(context.Context, Turn) (Transition, error) {
		return Yield(0, []Effect{effect}, kernelWait())
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	signal := answerSignal(kernelWait().Key, controlValue(EncodePayload(42)))
	if accepted, err := handle.DeliverSignals(ctx, signal); accepted || !errors.Is(err, cause) {
		t.Errorf("uncommitted signal claimed acceptance while draining: accepted=%t error=%v", accepted, err)
	}
	released.Do(func() { close(release) })
	if _, err := handle.Await(ctx); !errors.Is(err, cause) {
		t.Fatalf("drained writer lost the storage failure: %v", err)
	}
}

type childControlDefinition struct {
	descriptor Descriptor
	child      Program[int, int]
	control    func(ProcessID) (Effect, error)
}

func (c *childControlDefinition) Descriptor() Descriptor { return c.descriptor }
func (c *childControlDefinition) ChildPrograms() []Program[Payload, Payload] {
	return []Program[Payload, Payload]{c.child.Erase()}
}
func (c *childControlDefinition) Start(Payload) (Execution, error) {
	return &childControlExecution{definition: c}, nil
}
func (c *childControlDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	phase, err := state.Decode[childControlPhase]("test.child_control")
	if err != nil {
		return nil, err
	}
	return &childControlExecution{definition: c, phase: phase}, nil
}

type childControlExecution struct {
	definition *childControlDefinition
	phase      childControlPhase
}

type childControlPhase uint8

const (
	childControlPrepareSpawn childControlPhase = iota
	childControlAwaitSpawn
	childControlAwaitExit
)

func (c *childControlExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("test.child_control", c.phase)
}
func (c *childControlExecution) Step(_ context.Context, turn Turn) (Transition, error) {
	consumed := uint32(len(turn.Messages))
	switch c.phase {
	case childControlPrepareSpawn:
		c.phase = childControlAwaitSpawn
		spawn := controlValue(NewChildStartEffect(ChildSpec{Key: controlValue(ParseChildKey("controlled")), ProgramRef: c.definition.child.ProgramRef(), Input: controlValue(EncodePayload(0))}))
		return Yield(0, []Effect{spawn})
	case childControlAwaitSpawn:
		if len(turn.Messages) != 1 {
			return Transition{}, ErrInvalidMessage
		}
		completion, ok := turn.Messages[0].(Completion)
		if !ok || completion.Outcome.Status() != SettlementStatusSucceeded {
			return Transition{}, ErrInvalidMessage
		}
		process, err := controlValue(ParsePayload(completion.Outcome.Payload())).Decode[ProcessID]()
		if err != nil {
			return Transition{}, err
		}
		effect, err := c.definition.control(process)
		if err != nil {
			return Transition{}, err
		}
		c.phase = childControlAwaitExit
		return Yield(consumed, []Effect{effect})
	default:
		for _, message := range turn.Messages {
			if _, ok := message.(Exit); ok {
				return Complete(consumed, controlValue(EncodePayload(1)))
			}
		}
		return Yield(consumed, nil)
	}
}

func TestFrameworkSendAndCancelAcknowledgeTheirDurableChildChange(t *testing.T) {
	signal := Signal{ID: controlValue(ParseSignalID("child-input")), Payload: controlValue(EncodePayload(42))}
	for _, test := range []struct {
		name    string
		control func(ProcessID) (Effect, error)
		status  Status
	}{
		{"send", func(id ProcessID) (Effect, error) { return NewChildSignalEffect(id, signal) }, StatusCompleted},
		{"cancel", func(id ProcessID) (Effect, error) { return NewChildCancelEffect(id, "parent no longer needs child") }, StatusCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store})
			definition := &childControlDefinition{descriptor: kernelDescriptor(t, "test.child_control"), child: lifecycleEchoProgram(t, runtime, "test.controlled_child"), control: test.control}
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: test.name}))
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			result := controlValue(handle.Await(ctx))
			if output, err := result.Output(); err != nil || output != 1 || result.Usage().PreparedEffects != 2 {
				t.Fatalf("framework control did not drain: output=%d err=%v usage=%+v", output, err, result.Usage())
			}
			log, _, err := store.Load(ctx, handle.ID())
			if err != nil {
				t.Fatal(err)
			}
			var changed, acknowledged, exited LSN
			for _, entry := range log.Entries {
				switch record := entry.Record.(type) {
				case SignalAdmitted:
					if record.Process != handle.ID() && record.Signal.digest() == signal.digest() {
						changed = entry.LSN
					}
				case ControlRequested:
					if record.Process != handle.ID() && record.Kind == ControlCancel {
						changed = entry.LSN
					}
				case EffectSettled:
					if record.ID.Process == handle.ID() && record.ID.Step == 2 && record.Outcome.Status() == SettlementStatusSucceeded {
						acknowledged = entry.LSN
					}
				case Exited:
					if record.Process != handle.ID() {
						exited = entry.LSN
						if record.Termination.Status() != test.status {
							t.Fatalf("child status=%s", record.Termination.Status())
						}
					}
				}
			}
			if changed == 0 || acknowledged != changed+1 || exited <= acknowledged {
				t.Fatalf("framework change and acknowledgment were not adjacent before Exit: %d/%d/%d", changed, acknowledged, exited)
			}
		})
	}
}

func TestFrameworkControlsCannotActOnAnotherParentsChild(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	owner := treeFixtureProgram(t, runtime, 1, false, true, Budget{})
	owned := controlValue(runtime.Start(t.Context(), owner, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	child := controlValue(owned.ID().Child(controlValue(ParseChildKey("child-0"))))
	for entry, err := range owned.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if step, ok := entry.Record.(Stepped); ok && step.Process == child {
			break
		}
	}
	for _, test := range []struct {
		name    string
		control func(ProcessID) (Effect, error)
	}{
		{"send", func(id ProcessID) (Effect, error) {
			return NewChildSignalEffect(id, Signal{ID: controlValue(ParseSignalID("unauthorized")), Payload: controlValue(EncodePayload(42))})
		}},
		{"cancel", func(id ProcessID) (Effect, error) { return NewChildCancelEffect(id, "unauthorized cancellation") }},
	} {
		for _, target := range []string{"another parent", "missing child", "root"} {
			t.Run(test.name+" "+target, func(t *testing.T) {
				definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.control_authority"), step: func(_ context.Context, turn Turn) (Transition, error) {
					if turn.Step == 1 {
						recipient := child
						switch target {
						case "missing child":
							recipient = controlValue(turn.Process.Child(controlValue(ParseChildKey("absent"))))
						case "root":
							recipient = turn.Process
						}
						return Yield(0, []Effect{controlValue(test.control(recipient))})
					}
					completion, ok := turn.Messages[0].(Completion)
					if !ok || completion.Outcome.Status() != SettlementStatusFailed {
						return Transition{}, errors.New("unauthorized framework operation succeeded")
					}
					failure := controlValue(controlValue(ParsePayload(completion.Outcome.Payload())).Decode[Failure]())
					if failure.Kind() != FailureKindContract || failure.Code() != failureCodeRuntimeChildRequestInvalid {
						return Transition{}, errors.New("framework rejection lost its contract classification")
					}
					return Complete(1, controlValue(EncodePayload(1)))
				}}
				program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: test.name + target}))
				if output, err := runtime.Run(ctx, program, 0); err != nil || output != 1 {
					t.Fatalf("unauthorized operation=%d %v", output, err)
				}
				view := controlValue(owned.Inspect(ctx))
				process, exists := view.Process(child)
				if !exists || process.Status != StatusWaiting || process.Usage.AcceptedMessages != 0 {
					t.Fatalf("foreign operation changed owned child: %+v", process)
				}
			})
		}
	}
	if err := owned.Cancel(ctx, "test finished"); err != nil {
		t.Fatal(err)
	}
	if _, err := owned.Await(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMutatedProgramDeclarationsAreRejectedBeforeStartingWork(t *testing.T) {
	for _, target := range []string{"descriptor", "children", "build"} {
		t.Run(target, func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store})
			definition := &treeFixtureDefinition{descriptor: kernelDescriptor(t, "test.mutable_binding")}
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
			switch target {
			case "descriptor":
				definition.descriptor = kernelDescriptor(t, "test.changed_binding")
			case "children":
				definition.children = []Program[Payload, Payload]{lifecycleEchoProgram(t, runtime, "test.added_child").Erase()}
			case "build":
				other := kernelRuntime(t, Config{Build: "another-build"})
				program = controlValue(Bind[int, int](other.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
			}
			id := hostStartID()
			if _, err := runtime.Start(t.Context(), program, StartRequest[int]{ID: id}); !errors.Is(err, ErrInvalidProgram) {
				t.Fatalf("changed binding executed: %v", err)
			}
			if _, found, err := store.Load(t.Context(), id.Root()); err != nil || found {
				t.Fatalf("rejected program created a durable root: found=%t error=%v", found, err)
			}
		})
	}
}

func TestMutatedChildBindingCannotReachItsExecution(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	var calls atomic.Int32
	leaf := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.frozen_child"), step: func(context.Context, Turn) (Transition, error) {
		calls.Add(1)
		return Complete(0, controlValue(EncodePayload(1)))
	}}
	child := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: leaf, Revision: "one"}))
	parent := &childControlDefinition{descriptor: kernelDescriptor(t, "test.frozen_parent"), child: child, control: func(id ProcessID) (Effect, error) {
		return NewChildCancelEffect(id, "test finished")
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: parent, Revision: "one"}))
	leaf.descriptor = kernelDescriptor(t, "test.changed_child")
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	result := controlValue(handle.Await(ctx))
	if result.Termination().Status() != StatusFailed || calls.Load() != 0 {
		t.Fatal("mutated child binding crossed the execution boundary")
	}
	log, _, err := store.Load(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	rejected := false
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case Started:
			if record.Process != handle.ID() {
				t.Fatal("mutated child declaration was durably admitted")
			}
		case EffectSettled:
			if record.Outcome.Status() != SettlementStatusFailed {
				t.Fatal("mutated child declaration acquired a successful receipt")
			}
			failure := controlValue(controlValue(ParsePayload(record.Outcome.Payload())).Decode[Failure]())
			rejected = failure.Kind() == FailureKindContract && failure.Code() == failureCodeRuntimeChildRequestInvalid
		}
	}
	if !rejected {
		t.Fatal("mutated child declaration lost its definite admission rejection")
	}
}

func TestInvalidAnswerEnvelopesCannotConsumeTheWait(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	program := lifecycleEchoProgram(t, runtime, "test.answer_contract")
	handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: hostStartID()}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, handle, 1)
	before := controlValue(handle.Inspect(ctx)).Head
	for _, test := range []struct {
		name  string
		apply func() error
	}{
		{"zero wait", func() error { return handle.Answer(ctx, WaitKey{}, 42) }},
		{"unknown wait", func() error { return handle.Answer(ctx, controlValue(ParseWaitKey("other")), 42) }},
		{"answer schema", func() error { return handle.Answer(ctx, kernelWait().Key, "wrong type") }},
		{"unencodable answer", func() error { return handle.Answer(ctx, kernelWait().Key, make(chan int)) }},
		{"duplicate answer identity", func() error {
			signal := answerSignal(kernelWait().Key, controlValue(EncodePayload(42)))
			_, err := handle.DeliverSignals(ctx, signal, signal)
			return err
		}},
		{"two answers for one wait", func() error {
			first := answerSignal(kernelWait().Key, controlValue(EncodePayload(42)))
			second := first
			second.ID = controlValue(ParseSignalID("second-answer"))
			_, err := handle.DeliverSignals(ctx, first, second)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.apply(); err == nil {
				t.Fatal("invalid answer admitted")
			}
			view := controlValue(handle.Inspect(ctx))
			process, _ := view.Process(handle.ID())
			if view.Head != before || len(process.Waits) != 1 || process.Usage.AcceptedMessages != 0 {
				t.Fatal("invalid answer consumed the outstanding wait")
			}
		})
	}
	if err := handle.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	if result := controlValue(handle.Await(ctx)); controlValue(result.Output()) != 42 {
		t.Fatal("valid answer could not complete the untouched wait")
	}
}
