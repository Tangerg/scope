package runtest

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

var errConformanceCrash = errors.New("runtest: simulated append acknowledgment loss")

const conformanceTimeout = 10 * time.Second

type appendCut uint8

const (
	cutBeforeAppend appendCut = iota
	cutAfterAppend
)

type appendBarrier struct {
	store    run.Store
	kind     run.RecordKind
	cut      appendCut
	seen     chan run.Batch
	release  chan struct{}
	selected atomic.Bool
	closed   sync.Once
}

func newAppendBarrier(store run.Store, kind run.RecordKind, cut appendCut) *appendBarrier {
	return &appendBarrier{store: store, kind: kind, cut: cut, seen: make(chan run.Batch, 1), release: make(chan struct{})}
}

func (a *appendBarrier) Read(ctx context.Context, root run.ProcessID, after run.LSN) iter.Seq2[run.Entry, error] {
	return a.store.Read(ctx, root, after)
}

func (a *appendBarrier) Append(ctx context.Context, batch run.Batch) error {
	selected := false
	for _, record := range batch.Records {
		kind, _, err := run.EncodeRecord(record)
		if err != nil {
			return err
		}
		if kind == a.kind {
			selected = true
			break
		}
	}
	if !selected || !a.selected.CompareAndSwap(false, true) {
		return a.store.Append(ctx, batch)
	}
	if a.cut == cutAfterAppend {
		if err := a.store.Append(ctx, batch); err != nil {
			return err
		}
	}
	a.seen <- batch
	select {
	case <-a.release:
		return errConformanceCrash
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *appendBarrier) Load(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
	return a.store.Load(ctx, root)
}

func (a *appendBarrier) abort() { a.closed.Do(func() { close(a.release) }) }

func (a *appendBarrier) await(t *testing.T) run.Batch {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceTimeout)
	defer cancel()
	select {
	case batch := <-a.seen:
		return batch
	case <-ctx.Done():
		t.Fatalf("append barrier %s was not reached: %v", a.kind, ctx.Err())
		return run.Batch{}
	}
}

func runRecoveryConformance(t *testing.T, factory func(*testing.T) run.Store) {
	for _, kind := range []run.RecordKind{run.RecordKindStarted, run.RecordKindEffectIntent, run.RecordKindEffectSettled, run.RecordKindExited} {
		for _, cut := range []appendCut{cutBeforeAppend, cutAfterAppend} {
			t.Run(fmt.Sprintf("crash %s cut %d", kind, cut), func(t *testing.T) {
				checkRecoveryCut(t, factory(t), kind, cut)
			})
		}
	}
	t.Run("same identity effects replay after intent", func(t *testing.T) { checkReplayAfterIntent(t, factory(t)) })
	t.Run("durable observations survive takeover", func(t *testing.T) { checkObservationRecovery(t, factory(t)) })
	t.Run("local waits have no handshake", func(t *testing.T) { checkLocalWait(t, factory(t)) })
	t.Run("nondeterministic replay is rejected", func(t *testing.T) { checkNondeterministicReplay(t, factory(t)) })
}

// assertCutHead checks that a crash left exactly the acknowledged prefix.
func assertCutHead(t *testing.T, log run.Log, batch run.Batch, cut appendCut) {
	t.Helper()
	if cut == cutBeforeAppend && log.Head() != batch.After {
		t.Fatalf("before-append cut advanced head: %d, want %d", log.Head(), batch.After)
	}
	if cut == cutAfterAppend && log.Head() != batch.After+run.LSN(len(batch.Records)) {
		t.Fatal("lost acknowledgment did not leave the exact committed batch")
	}
}

func resolveRetainedUnknown(t *testing.T, recovered *run.Handle[run.Payload], root run.ProcessID) {
	t.Helper()
	waitForUnknownSettlement(t, recovered)
	inspection, err := recovered.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, ok := inspection.Process(root)
	if !ok || len(process.PendingEffects) != 1 || process.PendingEffects[0].Outcome.Status() != run.SettlementStatusUnknown {
		t.Fatalf("unacknowledged external result did not remain unknown: %+v", process)
	}
	result, err := run.EncodePayload("result")
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := run.NewSettlement(run.SettlementStatusSucceeded, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.ResolveUnknownEffect(t.Context(), process.PendingEffects[0].Request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
}

func checkRecoveryCut(t *testing.T, store run.Store, kind run.RecordKind, cut appendCut) {
	gate := newAppendBarrier(store, kind, cut)
	defer gate.abort()
	dispatcher := conformanceDispatcher(t, run.ReplayPolicyNever)
	definition := newRecoveryDefinition(t, recoveryEffect)
	if kind == run.RecordKindStarted {
		definition.mode = recoveryComplete
	}
	original, program := recoveryRuntime(t, gate, definition, dispatcher)
	started := make(chan recoveryStart, 1)
	input := conformanceInput(t)
	go func() {
		handle, err := original.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "recovery"}, Input: input})
		started <- recoveryStart{handle: handle, err: err}
	}()
	batch := gate.await(t)
	log, found, err := store.Load(t.Context(), batch.Root)
	if err != nil {
		t.Fatal(err)
	}
	if kind == run.RecordKindStarted && cut == cutBeforeAppend {
		if found {
			t.Fatal("unacknowledged start left durable records")
		}
		gate.abort()
		if result := awaitRecoveryStart(t, started); !errors.Is(result.err, errConformanceCrash) || result.handle != nil {
			t.Fatalf("failed start: %+v", result)
		}
		return
	}
	if !found {
		t.Fatal("committed prefix was lost")
	}
	if kind == run.RecordKindEffectIntent && len(dispatcher.Requests()) != 0 {
		t.Fatal("dispatcher ran before intent acknowledgment")
	}
	assertCutHead(t, log, batch, cut)
	recoveredRuntime, recoveredProgram := recoveryRuntime(t, store, definition, dispatcher)
	recovered, err := recoveredRuntime.Recover(t.Context(), recoveredProgram, batch.Root)
	if err != nil {
		t.Fatal(err)
	}
	// A dispatch whose result never reached the log cannot be known to have failed.
	if kind == run.RecordKindEffectIntent && cut == cutAfterAppend || kind == run.RecordKindEffectSettled && cut == cutBeforeAppend {
		resolveRetainedUnknown(t, recovered, batch.Root)
	}
	result, err := recovered.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("recovered outcome=%v error=%v", result.Termination().Status(), err)
	}
	wantCalls := 1
	if kind == run.RecordKindStarted || kind == run.RecordKindEffectIntent && cut == cutAfterAppend {
		wantCalls = 0
	}
	if len(dispatcher.Requests()) != wantCalls {
		t.Fatalf("dispatches=%d, want %d", len(dispatcher.Requests()), wantCalls)
	}
	gate.abort()
	start := awaitRecoveryStart(t, started)
	if start.err != nil {
		if !errors.Is(start.err, errConformanceCrash) {
			t.Fatal(start.err)
		}
		return
	}
	if _, err := start.handle.Await(t.Context()); !errors.Is(err, errConformanceCrash) {
		t.Fatalf("original writer outcome=%v", err)
	}
}

func checkReplayAfterIntent(t *testing.T, store run.Store) {
	gate := newAppendBarrier(store, run.RecordKindEffectIntent, cutAfterAppend)
	defer gate.abort()
	dispatcher := conformanceDispatcher(t, run.ReplayPolicySameIdentity)
	definition := newRecoveryDefinition(t, recoveryEffect)
	original, program := recoveryRuntime(t, gate, definition, dispatcher)
	handle, err := original.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "replay"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	batch := gate.await(t)
	var intended run.EffectID
	for _, record := range batch.Records {
		if intent, ok := record.(run.EffectIntent); ok {
			intended = intent.ID
		}
	}
	if !intended.Valid() || len(dispatcher.Requests()) != 0 {
		t.Fatal("intent cut did not precede dispatch")
	}
	recoveredRuntime, recoveredProgram := recoveryRuntime(t, store, definition, dispatcher)
	recovered, err := recoveredRuntime.Recover(t.Context(), recoveredProgram, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	requests := dispatcher.Requests()
	if len(requests) != 1 || requests[0].ID() != intended {
		t.Fatalf("replay identity changed: %v", requests)
	}
	gate.abort()
	if _, err := handle.Await(t.Context()); !errors.Is(err, errConformanceCrash) {
		t.Fatal(err)
	}
}

func checkObservationRecovery(t *testing.T, store run.Store) {
	definition := newRecoveryDefinition(t, recoveryWait)
	original, program := recoveryRuntime(t, store, definition, nil)
	handle, err := original.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "observe"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	waitForRecoveryWait(t, handle)
	before := loadStoreLog(t, store, handle.ID())
	prefix := collectObserved(t, handle, 0, before.Head())
	assertStoreEntries(t, prefix, before.Entries)
	if closeErr := original.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	recoveredRuntime, recoveredProgram := recoveryRuntime(t, store, definition, nil)
	recovered, err := recoveredRuntime.Recover(t.Context(), recoveredProgram, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Answer(t.Context(), conformanceWaitKey(t), "answer"); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := loadStoreLog(t, store, handle.ID())
	suffix := collectObserved(t, recovered, before.Head(), after.Head())
	assertStoreEntries(t, suffix, after.Entries[len(before.Entries):])
}

func checkLocalWait(t *testing.T, store run.Store) {
	definition := newRecoveryDefinition(t, recoveryWait)
	runtime, program := recoveryRuntime(t, store, definition, nil)
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "wait"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	waitForRecoveryWait(t, handle)
	if answerErr := handle.Answer(t.Context(), conformanceWaitKey(t), "answer"); answerErr != nil {
		t.Fatal(answerErr)
	}
	result, err := handle.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("wait result=%v error=%v", result.Termination().Status(), err)
	}
	log := loadStoreLog(t, store, handle.ID())
	steps := 0
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.EffectIntent:
			t.Fatal("local wait manufactured an effect")
		case run.Stepped:
			steps++
			if record.Step == 1 && record.Delivered != 0 || record.Step == 2 && record.Delivered != 1 {
				t.Fatalf("wait inbox had handshake messages: %+v", record)
			}
		}
	}
	if steps != 2 {
		t.Fatalf("wait required %d steps, want exactly opening and answer", steps)
	}
}

func checkNondeterministicReplay(t *testing.T, store run.Store) {
	definition := newRecoveryDefinition(t, recoveryUnstable)
	definition.counter = &atomic.Uint64{}
	runtime, program := recoveryRuntime(t, store, definition, nil)
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "unstable"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	recoveredRuntime, recoveredProgram := recoveryRuntime(t, store, definition, nil)
	if _, err := recoveredRuntime.Recover(t.Context(), recoveredProgram, handle.ID()); !errors.Is(err, run.ErrNondeterministicStep) {
		t.Fatalf("nondeterministic replay=%v", err)
	}
}

func waitForUnknownSettlement(t *testing.T, handle *run.Handle[run.Payload]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceTimeout)
	defer cancel()
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		settled, ok := entry.Record.(run.EffectSettled)
		if ok && settled.ID.Process == handle.ID() && settled.Outcome.Status() == run.SettlementStatusUnknown {
			return
		}
	}
	t.Fatal("unacknowledged effect did not receive a durable unknown outcome")
}

func collectObserved(t *testing.T, handle *run.Handle[run.Payload], after, through run.LSN) []run.Entry {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceTimeout)
	defer cancel()
	var entries []run.Entry
	for entry, err := range handle.Observe(ctx, after) {
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
		if entry.LSN == through {
			break
		}
	}
	return entries
}

func waitForRecoveryWait(t *testing.T, handle *run.Handle[run.Payload]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceTimeout)
	defer cancel()
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if step, ok := entry.Record.(run.Stepped); ok && step.Process == handle.ID() && len(step.Transition.Waits()) == 1 {
			return
		}
	}
	t.Fatal("no committed local wait was observed")
}

type recoveryStart struct {
	handle *run.Handle[run.Payload]
	err    error
}

func awaitRecoveryStart(t *testing.T, started <-chan recoveryStart) recoveryStart {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceTimeout)
	defer cancel()
	select {
	case result := <-started:
		return result
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return recoveryStart{}
	}
}

func recoveryRuntime(t *testing.T, store run.Store, definition *recoveryDefinition, dispatcher run.Dispatcher) (*run.Runtime, run.Program[run.Payload, run.Payload]) {
	t.Helper()
	runtime, err := run.New(t.Context(), run.Config{Store: store, Build: conformanceBuild})
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
	program, err := run.Bind[run.Payload, run.Payload](runtime.Compiler(), run.BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one", Configuration: definition.mode})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, program
}

func conformanceDispatcher(t *testing.T, replay run.ReplayPolicy) *ScriptedDispatcher {
	t.Helper()
	settlement := conformanceSettlement(t, run.SettlementStatusSucceeded, "result")
	dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{ReplayPolicy: replay, Calls: []ScriptedCall{{Settlement: settlement}}})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func conformanceSettlement(t *testing.T, status run.SettlementStatus, value any) run.Settlement {
	t.Helper()
	settlement, err := run.NewSettlement(status, conformancePayload(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return settlement
}

func conformanceInput(t *testing.T) run.Payload {
	t.Helper()
	return conformancePayload(t, "input")
}

func conformancePayload(t *testing.T, value any) run.Payload {
	t.Helper()
	payload, err := run.EncodePayload(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func conformanceWaitKey(t *testing.T) run.WaitKey {
	t.Helper()
	key, err := run.ParseWaitKey("answer")
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type recoveryMode string

const (
	recoveryComplete recoveryMode = "complete"
	recoveryEffect   recoveryMode = "effect"
	recoveryWait     recoveryMode = "wait"
	recoveryUnstable recoveryMode = "unstable"
)

type recoveryDefinition struct {
	descriptor run.Descriptor
	mode       recoveryMode
	counter    *atomic.Uint64
}

func newRecoveryDefinition(t *testing.T, mode recoveryMode) *recoveryDefinition {
	t.Helper()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "runtest.recovery", Description: "Exercise committed log recovery.", InputSchema: schema, OutputSchema: schema, SignalSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	return &recoveryDefinition{descriptor: descriptor, mode: mode}
}

func (r *recoveryDefinition) Descriptor() run.Descriptor                             { return r.descriptor }
func (r *recoveryDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (r *recoveryDefinition) Start(input run.Payload) (run.Execution, error) {
	return &recoveryExecution{definition: r, state: recoveryState{Input: input}}, nil
}

func (r *recoveryDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[recoveryState]("runtest.recovery")
	if err != nil {
		return nil, err
	}
	return &recoveryExecution{definition: r, state: value}, nil
}

type recoveryState struct {
	Input   run.Payload `json:"input"`
	Waiting bool        `json:"waiting"`
}

type recoveryExecution struct {
	definition *recoveryDefinition
	state      recoveryState
}

func (r *recoveryExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	consumed := uint32(len(turn.Messages))
	switch r.definition.mode {
	case recoveryComplete:
		return run.Complete(consumed, r.state.Input)
	case recoveryUnstable:
		output, err := run.EncodePayload(fmt.Sprint(r.definition.counter.Add(1)))
		if err != nil {
			return run.Transition{}, err
		}
		return run.Complete(consumed, output)
	case recoveryEffect:
		if !r.state.Waiting {
			r.state.Waiting = true
			call, err := run.EncodePayload("call")
			if err != nil {
				return run.Transition{}, err
			}
			effect, err := run.NewDispatcherEffect(call)
			if err != nil {
				return run.Transition{}, err
			}
			return run.Yield(consumed, []run.Effect{effect})
		}
		if len(turn.Messages) == 0 {
			return run.Yield(0, nil)
		}
		if len(turn.Messages) != 1 {
			return run.Transition{}, run.ErrInvalidMessage
		}
		completion, ok := turn.Messages[0].(run.Completion)
		if !ok || completion.Outcome.Status() != run.SettlementStatusSucceeded {
			return run.Transition{}, run.ErrInvalidMessage
		}
		payload := completion.Outcome.Payload()
		return run.Complete(consumed, payload)
	case recoveryWait:
		key, err := run.ParseWaitKey("answer")
		if err != nil {
			return run.Transition{}, err
		}
		if !r.state.Waiting {
			r.state.Waiting = true
			return run.Yield(consumed, nil, run.Wait{Key: key})
		}
		if len(turn.Messages) != 1 {
			return run.Transition{}, run.ErrInvalidMessage
		}
		signal, ok := turn.Messages[0].(run.Signal)
		if !ok || signal.Wait != key {
			return run.Transition{}, run.ErrInvalidMessage
		}
		return run.Complete(consumed, signal.Payload)
	default:
		return run.Transition{}, run.ErrInvalidTransition
	}
}

func (r *recoveryExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("runtest.recovery", r.state)
}
