package run

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestDrainingProcessDoesNotAdvertiseUnanswerableWait(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	effect := controlValue(NewDispatcherEffect(controlValue(ParsePayload([]byte(`0`)))))
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
		return NewSettlement(SettlementStatusUnknown, controlValue(ParsePayload([]byte(`null`))))
	}}
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.draining_wait"), step: func(context.Context, Turn) (Transition, error) {
		return Yield(0, []Effect{effect}, kernelWait())
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	lifecycleUnknown(t, ctx, handle)
	if err := handle.Cancel(ctx, "user canceled"); err != nil {
		t.Fatal(err)
	}
	view := controlValue(runtime.Inspect(ctx, handle.ID()))
	process, ok := view.Process(handle.ID())
	if !ok || process.Status != StatusDraining {
		t.Fatalf("expected draining process: %+v", process)
	}
	answerErr := handle.Answer(ctx, kernelWait().Key, 42)
	if !errors.Is(answerErr, ErrProcessFinished) {
		t.Fatalf("expected unanswerable canceled wait: %v", answerErr)
	}
	if len(process.Waits) != 0 {
		t.Fatalf("inspection advertises %d waits that reject answers with %v", len(process.Waits), answerErr)
	}
}

func TestForegroundRunReleasesAnonymousLocalTrees(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.foreground_retention"), step: func(context.Context, Turn) (Transition, error) {
		return Complete(0, controlValue(EncodePayload(42)))
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
	for range 32 {
		if output, err := runtime.Run(t.Context(), program, 0); err != nil || output != 42 {
			t.Fatalf("foreground run failed: %d %v", output, err)
		}
	}
	runtime.mu.RLock()
	var trees []*treeRuntime
	for _, tree := range runtime.roots {
		trees = append(trees, tree)
	}
	runtime.mu.RUnlock()
	for _, tree := range trees {
		<-tree.done
	}
	runtime.mu.RLock()
	retained := len(runtime.roots)
	runtime.mu.RUnlock()
	if retained != 0 {
		t.Fatalf("%d completed Runtime.Run calls retained %d anonymous local trees; Run exposes no release identity", 32, retained)
	}
}

func TestChildCancelRetainsParentOrigin(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	definition := &childControlDefinition{
		descriptor: kernelDescriptor(t, "test.parent_cancel"),
		child:      lifecycleEchoProgram(t, runtime, "test.child"),
		control:    func(id ProcessID) (Effect, error) { return NewChildCancelEffect(id, "parent no longer needs child") },
	}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	if _, err := handle.Await(ctx); err != nil {
		t.Fatal(err)
	}
	log, _, err := store.Load(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, entry := range log.Entries {
		if record, ok := entry.Record.(Exited); ok && record.Process != handle.ID() {
			found = true
			if got := record.Termination.Cause(); got != TerminationCauseParentCancellation {
				t.Errorf("parent-issued ChildCancel effect recorded cause %q, want %q", got, TerminationCauseParentCancellation)
			}
		}
	}
	if !found {
		t.Fatal("missing child Exited")
	}
}

func TestForegroundRunJoinsDispatchAndRetainsRecoveryIdentity(t *testing.T) {
	entered := make(chan EffectID, 1)
	var joined atomic.Bool
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(ctx context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
		defer joined.Store(true)
		entered <- request.ID()
		<-ctx.Done()
		return Settlement{}, ctx.Err()
	}}
	program := lifecycleCallProgram(t, runtime, dispatcher)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := runtime.Run(ctx, program, 0)
		returned <- err
	}()
	observation, observationCancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer observationCancel()
	var id EffectID
	select {
	case id = <-entered:
	case <-observation.Done():
		t.Fatal(observation.Err())
	}
	cancel()
	select {
	case err := <-returned:
		var interrupted *RuntimeError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &interrupted) || interrupted.ProcessID() != id.Process || len(interrupted.UnresolvedEffectIDs()) != 1 || interrupted.UnresolvedEffectIDs()[0] != id || !joined.Load() {
			t.Fatalf("Run returned before joining or lost unresolved work: joined=%v error=%v", joined.Load(), err)
		}
	case <-observation.Done():
		t.Fatal(observation.Err())
	}
	if _, present := runtime.Process(id.Process); present {
		t.Fatal("Run retained an interrupted anonymous tree")
	}
	log, found, err := store.Load(observation, id.Process)
	if err != nil || !found || log.Validate() != nil {
		t.Fatalf("Run lost its recoverable log: found=%v error=%v", found, err)
	}
	view := controlValue(runtime.Inspect(observation, id.Process))
	process, found := view.Process(id.Process)
	if !found || process.Status != StatusDraining || len(process.PendingEffects) != 1 {
		t.Fatalf("interrupted Run invented a definite result: %+v", process)
	}
}

func TestForegroundRunRetainsIdentityWhenTheInitialCommitAcknowledgmentIsLost(t *testing.T) {
	store := NewMemoryStore()
	cause := errors.New("initial acknowledgment lost")
	fault := &lifecycleFaultStore{store: store, kind: RecordKindStarted, cause: cause, commitFirst: true}
	runtime := kernelRuntime(t, Config{Store: fault})
	program := lifecycleEchoProgram(t, runtime, "test.foreground_initial_commit")
	_, err := runtime.Run(t.Context(), program, 0)
	var interrupted *RuntimeError
	if !errors.Is(err, cause) || !errors.As(err, &interrupted) || !interrupted.ProcessID().Valid() || interrupted.After() != 0 {
		t.Fatalf("initial failure lost the unacknowledged tree identity: %v", err)
	}
	if _, present := runtime.Process(interrupted.ProcessID()); present {
		t.Fatal("failed launch acquired a local writer")
	}
	recovered := controlValue(runtime.Recover(t.Context(), program, interrupted.ProcessID()))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	waitForKernelStep(t, ctx, recovered, 1)
	if err := recovered.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	result := controlValue(recovered.Await(ctx))
	if output := controlValue(result.Output()); output != 42 {
		t.Fatalf("recovered output=%d", output)
	}
}

func TestCancellationCauseUsesTheRequesterAndDescendantRelation(t *testing.T) {
	root := controlValue(ParseProcessID("root"))
	child := controlValue(root.Child(controlValue(ParseChildKey("child"))))
	grandchild := controlValue(child.Child(controlValue(ParseChildKey("grandchild"))))
	for _, test := range []struct {
		name   string
		target ProcessID
		origin ControlOrigin
		kind   ControlKind
		want   TerminationCause
	}{
		{"host child", child, ControlOriginHost, ControlCancel, TerminationCauseHostCancellation},
		{"parent child", child, ControlOriginParent, ControlCancel, TerminationCauseParentCancellation},
		{"host root deadline", root, ControlOriginHost, ControlDeadline, TerminationCauseHostDeadline},
		{"parent deadline", child, ControlOriginParent, ControlDeadline, TerminationCauseParentDeadline},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newTreeState(root)
			for _, id := range []ProcessID{root, child, grandchild} {
				state.processes[id] = newProcessState(Started{Process: id})
			}
			state.terminate(ControlRequested{Process: test.target, Origin: test.origin, Kind: test.kind, Reason: "stop"})
			if cause := state.processes[test.target].terminal.Cause(); cause != test.want {
				t.Fatalf("target cause=%s want=%s", cause, test.want)
			}
			wantDescendant := TerminationCauseParentCancellation
			if test.kind == ControlDeadline {
				wantDescendant = TerminationCauseParentDeadline
			}
			if cause := state.processes[grandchild].terminal.Cause(); cause != wantDescendant {
				t.Fatalf("descendant cause=%s want=%s", cause, wantDescendant)
			}
		})
	}
}
