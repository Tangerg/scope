package runtest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func runRuntimeConformance(t *testing.T, factory func(*testing.T) run.Store) {
	t.Run("start retries do not activate another writer", func(t *testing.T) { checkStartRetry(t, factory(t)) })
	t.Run("signal receipts survive recovery", func(t *testing.T) { checkSignalReceiptRecovery(t, factory(t)) })
	t.Run("closing joins in-flight root admissions", func(t *testing.T) { checkCloseDuringStart(t, factory(t)) })
	t.Run("settled effect intent cannot be repeated", func(t *testing.T) { checkRepeatedEffectIntent(t, factory(t)) })
	t.Run("initial record respects its declared byte bound", func(t *testing.T) { checkInitialRecordLimit(t, factory(t)) })
	t.Run("loads cannot substitute another root", func(t *testing.T) { checkLoadRootIsolation(t, factory(t)) })
	t.Run("canceling uncertainty waits for reconciliation", func(t *testing.T) { checkUnresolvedCancellation(t, factory(t)) })
	t.Run("checkpoint recovery keeps the same local wait", func(t *testing.T) { checkCheckpointWait(t, factory(t)) })
}

func checkStartRetry(t *testing.T, store run.Store) {
	definition := newRecoveryDefinition(t, recoveryWait)
	original, program := recoveryRuntime(t, store, definition, nil)
	request := run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "start-retry"}, Input: conformanceInput(t)}
	first, err := original.Start(t.Context(), program, request)
	if err != nil {
		t.Fatal(err)
	}
	waitForRecoveryWait(t, first)
	before := loadStoreLog(t, store, first.ID())
	replacement, sameProgram := recoveryRuntime(t, store, definition, nil)
	retry, err := replacement.Start(t.Context(), sameProgram, request)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID() != first.ID() || retry.Epoch() != first.Epoch() {
		t.Fatal("retry changed the root or its writer epoch")
	}
	after := loadStoreLog(t, store, first.ID())
	assertStoreEntries(t, after.Entries, before.Entries)
	changed, err := run.EncodePayload("another input")
	if err != nil {
		t.Fatal(err)
	}
	request.Input = changed
	var conflict *run.StartConflictError
	if _, startErr := replacement.Start(t.Context(), sameProgram, request); !errors.As(startErr, &conflict) {
		t.Fatalf("conflicting start result=%v", startErr)
	}
	if answerErr := first.Answer(t.Context(), conformanceWaitKey(t), "answer"); answerErr != nil {
		t.Fatal(answerErr)
	}
	result, err := retry.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("retry observation status=%s error=%v", result.Termination().Status(), err)
	}
}

func checkSignalReceiptRecovery(t *testing.T, store run.Store) {
	definition := newRecoveryDefinition(t, recoveryWait)
	original, program := recoveryRuntime(t, store, definition, nil)
	handle, err := original.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "signal-retry"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	waitForRecoveryWait(t, handle)
	if pauseErr := handle.Pause(t.Context(), "hold the answer for recovery"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	signalID, err := run.ParseSignalID("one-answer")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := run.EncodePayload("answer")
	if err != nil {
		t.Fatal(err)
	}
	signal := run.Signal{ID: signalID, Wait: conformanceWaitKey(t), Payload: payload}
	if accepted, signalErr := handle.DeliverSignals(t.Context(), signal); signalErr != nil || !accepted {
		t.Fatalf("first signal accepted=%t error=%v", accepted, signalErr)
	}
	before := loadStoreLog(t, store, handle.ID())
	if accepted, signalErr := handle.DeliverSignals(t.Context(), signal); signalErr != nil || accepted {
		t.Fatalf("duplicate signal accepted=%t error=%v", accepted, signalErr)
	}
	changed, err := run.EncodePayload("conflicting answer")
	if err != nil {
		t.Fatal(err)
	}
	conflict := signal
	conflict.Payload = changed
	if _, signalErr := handle.DeliverSignals(t.Context(), conflict); !errors.Is(signalErr, run.ErrSignalConflict) {
		t.Fatalf("conflicting signal=%v", signalErr)
	}
	if _, signalErr := handle.DeliverSignals(t.Context(), signal, run.Signal{}); !errors.Is(signalErr, run.ErrInvalidMessage) {
		t.Fatalf("partially invalid signal batch=%v", signalErr)
	}
	assertStoreEntries(t, loadStoreLog(t, store, handle.ID()).Entries, before.Entries)
	if closeErr := original.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	replacement, restoredProgram := recoveryRuntime(t, store, definition, nil)
	restored, err := replacement.Recover(t.Context(), restoredProgram, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	if accepted, signalErr := restored.DeliverSignals(t.Context(), signal); signalErr != nil || accepted {
		t.Fatalf("recovered signal retry accepted=%t error=%v", accepted, signalErr)
	}
	if resumeErr := restored.Resume(t.Context()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	result, err := restored.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("recovered answer status=%s error=%v", result.Termination().Status(), err)
	}
	if result.Usage().AcceptedMessages != 1 {
		t.Fatalf("one answer counted as %d messages", result.Usage().AcceptedMessages)
	}
}

func checkCloseDuringStart(t *testing.T, store run.Store) {
	entered, returned, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	admitter := run.ProcessAdmitterFunc(func(ctx context.Context, _ run.ProcessAdmission) error {
		close(entered)
		defer close(returned)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	})
	runtime, err := run.New(t.Context(), run.Config{Build: conformanceBuild, Store: store, ProcessAdmitter: admitter})
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
	program, err := run.Bind[string, string](runtime.Compiler(), run.BindingConfig{Definition: newStoreDefinition(t), Revision: "one"})
	if err != nil {
		t.Fatal(err)
	}
	id := run.StartID{Scope: "runtest", Key: "close-start"}
	started := make(chan error, 1)
	go func() {
		_, startErr := runtime.Start(t.Context(), program, run.StartRequest[string]{ID: id, Input: "input"})
		started <- startErr
	}()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceTimeout)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		close(release)
		t.Fatal("root admission was never called")
	}
	closeErr := runtime.Close(ctx)
	select {
	case <-returned:
	default:
		t.Error("Runtime.Close returned before its root admission stopped")
	}
	close(release)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	select {
	case startErr := <-started:
		if startErr == nil {
			t.Fatal("closed runtime acknowledged a new root")
		}
	case <-ctx.Done():
		t.Fatal("closed runtime left Start running")
	}
	if _, found, loadErr := store.Load(t.Context(), id.Root()); loadErr != nil || found {
		t.Fatalf("closed runtime appended after Close: found=%t error=%v", found, loadErr)
	}
}

func checkRepeatedEffectIntent(t *testing.T, store run.Store) {
	runtime, program := recoveryRuntime(t, store, newRecoveryDefinition(t, recoveryEffect), conformanceDispatcher(t, run.ReplayPolicyNever))
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "repeated-intent"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	log := loadStoreLog(t, store, handle.ID())
	var intent run.EffectIntent
	for index, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.EffectIntent:
			intent = record
		case run.EffectSettled:
			prefix := append([]run.Entry(nil), log.Entries[:index+1]...)
			prefix = append(prefix, run.Entry{LSN: entry.LSN + 1, Epoch: entry.Epoch, Record: intent})
			if validateErr := (run.Log{Root: log.Root, Entries: prefix}).Validate(); !errors.Is(validateErr, run.ErrInvalidLog) {
				t.Fatalf("settled effect accepted a second intent: %v", validateErr)
			}
			return
		}
	}
	t.Fatal("effect fixture emitted no settlement")
}

func checkInitialRecordLimit(t *testing.T, store run.Store) {
	const recordLimit = 1024
	runtime, err := run.New(t.Context(), run.Config{Build: conformanceBuild, Store: store, TreeLimits: run.TreeLimits{MaxRecordBytes: run.NewQuota(recordLimit)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	program, err := run.Bind[string, string](runtime.Compiler(), run.BindingConfig{Definition: newStoreDefinition(t), Revision: "one"})
	if err != nil {
		t.Fatal(err)
	}
	id := run.StartID{Scope: "runtest", Key: "oversized-start"}
	if _, startErr := runtime.Start(t.Context(), program, run.StartRequest[string]{ID: id, Input: strings.Repeat("x", recordLimit*2)}); !errors.Is(startErr, run.ErrResourceLimitExceeded) {
		t.Fatalf("oversized initial record=%v", startErr)
	}
	if _, found, loadErr := store.Load(t.Context(), id.Root()); loadErr != nil || found {
		t.Fatalf("oversized start committed a log: found=%t error=%v", found, loadErr)
	}
}

func checkLoadRootIsolation(t *testing.T, store run.Store) {
	definition := newRecoveryDefinition(t, recoveryComplete)
	original, program := recoveryRuntime(t, store, definition, nil)
	first, err := original.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "first-root"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := first.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	before := loadStoreLog(t, store, first.ID())
	replacement, sameProgram := recoveryRuntime(t, redirectedStore{store: store, target: first.ID()}, definition, nil)
	other := run.StartID{Scope: "runtest", Key: "other-root"}
	if _, startErr := replacement.Start(t.Context(), sameProgram, run.StartRequest[run.Payload]{ID: other, Input: conformanceInput(t)}); !errors.Is(startErr, run.ErrInvalidLog) {
		t.Fatalf("Start accepted a substituted root: %v", startErr)
	}
	if _, recoverErr := replacement.Recover(t.Context(), sameProgram, other.Root()); !errors.Is(recoverErr, run.ErrInvalidLog) {
		t.Fatalf("Recover accepted a substituted root: %v", recoverErr)
	}
	assertStoreEntries(t, loadStoreLog(t, store, first.ID()).Entries, before.Entries)
}

type redirectedStore struct {
	store  run.Store
	target run.ProcessID
}

func (r redirectedStore) Append(ctx context.Context, batch run.Batch) error {
	return r.store.Append(ctx, batch)
}
func (r redirectedStore) Load(ctx context.Context, _ run.ProcessID) (run.Log, bool, error) {
	return r.store.Load(ctx, r.target)
}

func checkUnresolvedCancellation(t *testing.T, store run.Store) {
	dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{SettlementStatus: run.SettlementStatusUnknown, SettlementPayload: []byte("null")}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, program := recoveryRuntime(t, store, newRecoveryDefinition(t, recoveryEffect), dispatcher)
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "cancel-unknown"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	waitForUnknownSettlement(t, handle)
	if cancelErr := handle.Cancel(t.Context(), "stop after the external outcome is reconciled"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	log := loadStoreLog(t, store, handle.ID())
	for _, entry := range log.Entries {
		if _, exited := entry.Record.(run.Exited); exited {
			t.Fatal("cancellation published Exit before the unknown outcome was resolved")
		}
	}
	inspection, err := handle.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, found := inspection.Process(handle.ID())
	if !found || len(process.PendingEffects) != 1 || process.PendingEffects[0].Outcome.Status() != run.SettlementStatusUnknown {
		t.Fatal("cancellation discarded the unknown effect")
	}
	settlement, err := run.NewSettlement(run.SettlementStatusSucceeded, []byte(`"confirmed"`))
	if err != nil {
		t.Fatal(err)
	}
	if resolveErr := handle.ResolveUnknownEffect(t.Context(), process.PendingEffects[0].Request.ID(), settlement); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	result, err := handle.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCanceled || len(dispatcher.Requests()) != 1 {
		t.Fatalf("reconciled cancellation status=%s calls=%d error=%v", result.Termination().Status(), len(dispatcher.Requests()), err)
	}
}

func checkCheckpointWait(t *testing.T, store run.Store) {
	gate := newAppendBarrier(store, run.RecordKindCheckpoint, cutAfterAppend)
	defer gate.abort()
	definition := newRecoveryDefinition(t, recoveryWait)
	original, err := run.New(t.Context(), run.Config{Build: conformanceBuild, Store: gate, CheckpointInterval: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), conformanceTimeout)
		defer cancel()
		if closeErr := original.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	program, err := run.Bind[run.Payload, run.Payload](original.Compiler(), run.BindingConfig{Definition: definition, Revision: "one", Configuration: definition.mode})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := original.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "checkpoint-wait"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	gate.await(t)
	if _, _, found := loadStoreLog(t, store, handle.ID()).LatestCheckpoint(); !found {
		t.Fatal("checkpoint barrier did not commit its recovery baseline")
	}
	replacement, restoredProgram := recoveryRuntime(t, store, definition, nil)
	restored, err := replacement.Recover(t.Context(), restoredProgram, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := restored.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, found := inspection.Process(handle.ID())
	if !found || len(process.Waits) != 1 || process.Waits[0].Key != conformanceWaitKey(t) {
		t.Fatal("checkpoint recovery changed the unanswered local wait")
	}
	if answerErr := restored.Answer(t.Context(), process.Waits[0].Key, "answer"); answerErr != nil {
		t.Fatal(answerErr)
	}
	if _, awaitErr := restored.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	gate.abort()
	if _, awaitErr := handle.Await(t.Context()); !errors.Is(awaitErr, errConformanceCrash) {
		t.Fatalf("original checkpoint writer result=%v", awaitErr)
	}
}
