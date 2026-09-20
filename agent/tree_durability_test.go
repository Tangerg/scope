package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recordingTreeCommitter struct {
	mu          sync.Mutex
	activations []TreeActivation
	effects     []EffectBoundary
	checkpoints []TreeCheckpoint
	pending     atomic.Bool
}

func (r *recordingTreeCommitter) ActivateTree(
	_ context.Context,
	activation TreeActivation,
) error {
	r.mu.Lock()
	r.activations = append(r.activations, activation)
	r.mu.Unlock()
	return nil
}

func (r *recordingTreeCommitter) CommitEffect(
	_ context.Context,
	boundary EffectBoundary,
) error {
	r.mu.Lock()
	r.effects = append(r.effects, boundary)
	r.mu.Unlock()
	if boundary.Kind() == EffectBoundaryKindPending {
		r.pending.Store(true)
	}
	return nil
}

func (r *recordingTreeCommitter) CommitCheckpoint(
	_ context.Context,
	checkpoint TreeCheckpoint,
) error {
	r.mu.Lock()
	r.checkpoints = append(r.checkpoints, checkpoint)
	r.mu.Unlock()
	return nil
}

func (r *recordingTreeCommitter) effectBoundaries() []EffectBoundary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]EffectBoundary(nil), r.effects...)
}

func (r *recordingTreeCommitter) treeCheckpoints() []TreeCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]TreeCheckpoint(nil), r.checkpoints...)
}

func (r *recordingTreeCommitter) treeActivations() []TreeActivation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]TreeActivation(nil), r.activations...)
}

type rejectingEffectDurability struct {
	*recordingTreeCommitter
	rejectedKind EffectBoundaryKind
	err          error
}

type blockingTerminalCheckpointDurability struct {
	*recordingTreeCommitter
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingChildAdmission struct {
	childKey ChildKey
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (b *blockingChildAdmission) Admit(
	ctx context.Context,
	admission ProcessAdmission,
) error {
	key, child := admission.Relation().ChildKey()
	if !child || key != b.childKey {
		return nil
	}
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *blockingTerminalCheckpointDurability) CommitCheckpoint(
	ctx context.Context,
	checkpoint TreeCheckpoint,
) error {
	if checkpoint.Kind() == TreeCheckpointKindTerminal {
		b.once.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.recordingTreeCommitter.CommitCheckpoint(ctx, checkpoint)
}

type typedNilTreeCommitter struct{}

func (*typedNilTreeCommitter) ActivateTree(context.Context, TreeActivation) error {
	return nil
}

func (*typedNilTreeCommitter) CommitEffect(context.Context, EffectBoundary) error {
	return nil
}

func (*typedNilTreeCommitter) CommitCheckpoint(context.Context, TreeCheckpoint) error {
	return nil
}

func TestTreeCommitterConfigurationIsUnambiguous(t *testing.T) {
	if _, err := NewEngine(EngineConfig{}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("missing committer: %v", err)
	}
	var typedNil *typedNilTreeCommitter
	if _, err := NewEngine(EngineConfig{TreeCommitter: typedNil}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("typed-nil TreeCommitter error=%v", err)
	}
	committer := &recordingTreeCommitter{}
	acknowledger := ProcessInitializationOutcomeAcknowledgerFunc(func(
		context.Context,
		ProcessInitializationOutcome,
	) error {
		return nil
	})
	if _, err := NewEngine(EngineConfig{
		TreeCommitter: committer, ProcessInitializationOutcomeAcknowledger: acknowledger,
	}); err != nil {
		t.Fatalf("independent committer and initialization ports error=%v", err)
	}
}

func TestRestoreRequiresAnAuthoritativeHead(t *testing.T) {
	tree := completedTreeSnapshot(t)
	engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
	_, err := engine.RestoreTree(t.Context(), engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), &engineTestDispatcher{policy: ReplayPolicyNever}), tree)
	if !errors.Is(err, ErrTreeIncarnationConflict) {
		t.Fatalf("restore without head = %v", err)
	}
}

func TestRestoreTreeRejectsLocalRegistrationBeforeActivation(t *testing.T) {
	committer := &recordingTreeCommitter{}
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(
		t, definition, &engineTestDispatcher{policy: ReplayPolicyNever},
	)
	source, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(engineTestInput{Value: "restore reservation"})
	original, err := source.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, original, StatusWaiting)
	parked := waitForDurableCheckpoint(t, committer, original.ID(), TreeCheckpointKindParked)

	destination, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := destination.RestoreTree(context.Background(), deployment, parked)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(committer.treeActivations()); got != 1 {
		t.Fatalf("activation count=%d, want 1", got)
	}
	if _, err := destination.RestoreTree(
		context.Background(), deployment, parked,
	); !errors.Is(err, ErrProcessAlreadyExists) {
		t.Fatalf("duplicate RestoreTree error=%v", err)
	}
	if got := len(committer.treeActivations()); got != 1 {
		t.Fatalf("duplicate restore reached activation; count=%d", got)
	}

	_ = restored.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, restored)
	_ = original.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, original)
	if err := destination.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func waitForDurableCheckpoint(
	t *testing.T,
	committer *recordingTreeCommitter,
	rootID ProcessID,
	kind TreeCheckpointKind,
) TreeSnapshot {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		for _, checkpoint := range committer.treeCheckpoints() {
			if checkpoint.Kind() == kind && checkpoint.TreeSnapshot().RootID() == rootID {
				return checkpoint.TreeSnapshot()
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("%s checkpoint for tree %s was not committed", kind, rootID)
		}
	}
}

func (r *rejectingEffectDurability) CommitEffect(
	ctx context.Context,
	boundary EffectBoundary,
) error {
	if err := r.recordingTreeCommitter.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	if boundary.Kind() == r.rejectedKind {
		return r.err
	}
	return nil
}

func TestDurableEffectCommitFailuresStopTheTreeAtTheCorrectBoundary(t *testing.T) {
	for _, test := range []struct {
		name             string
		kind             EffectBoundaryKind
		cause            error
		wantDispatches   int32
		wantUnresolvedID bool
		wantFailureKind  FailureKind
		wantFailureCode  string
	}{
		{
			name: "pending is definitely undispatched", kind: EffectBoundaryKindPending,
			cause:           errors.New("committer unavailable"),
			wantFailureKind: FailureKindExternal, wantFailureCode: failureCodeEngineTreeCommitterFailed,
		},
		{
			name: "settled preserves ambiguous Effect identity", kind: EffectBoundaryKindSettled,
			cause: errors.New("committer unavailable"), wantDispatches: 1,
			wantUnresolvedID: true, wantFailureKind: FailureKindExternal,
			wantFailureCode: failureCodeEngineTreeCommitterFailed,
		},
		{
			name: "content conflict is a Host contract violation", kind: EffectBoundaryKindPending,
			cause: ErrCommitConflict, wantFailureKind: FailureKindContract,
			wantFailureCode: failureCodeEngineTreeCommitterConflict,
		},
		{
			name: "stale writer is fenced", kind: EffectBoundaryKindPending,
			cause: ErrTreeIncarnationConflict, wantFailureKind: FailureKindExternal,
			wantFailureCode: failureCodeEngineTreeIncarnationConflict,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingTreeCommitter{}
			committer := &rejectingEffectDurability{
				recordingTreeCommitter: recorder, rejectedKind: test.kind, err: test.cause,
			}
			dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
			definition := newEngineTestDefinition(t, "engine.effect", "effect")
			deployment := engineTestDeployment(t, definition, dispatcher)
			listener := &recordingEventListener{}
			engine, err := NewEngine(EngineConfig{TreeCommitter: committer, EventListeners: []EventListener{listener}})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := EncodePayload(engineTestInput{Value: "committer"})
			process, err := engine.Start(context.Background(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			runtimeErr := awaitRuntimeError(t, process, test.cause)
			stopped := 0
			for _, event := range listener.snapshot() {
				if event.Name() == EventProcessFinished {
					t.Fatal("runtime failure published a logical terminal event")
				}
				if fact, ok := event.RuntimeStopped(); ok {
					stopped++
					if event.Phase() != EventPhaseAttempt || fact.FailureKind() != test.wantFailureKind || fact.FailureCode() != test.wantFailureCode {
						t.Fatalf("runtime stop observation=%+v", fact)
					}
				}
			}
			if stopped != 1 {
				t.Fatalf("runtime stop observations=%d, want 1", stopped)
			}
			head := recorder.treeCheckpoints()[0].TreeSnapshot()
			if test.kind == EffectBoundaryKindSettled {
				head = recorder.effectBoundaries()[0].TreeSnapshot()
			}
			if runtimeErr.HeadDigest() != head.Digest() || inspectProcessSnapshot(t, process).Status() != StatusRunning {
				t.Fatalf("runtime failure changed acknowledged state: digest=%s status=%s", runtimeErr.HeadDigest(), inspectProcessSnapshot(t, process).Status())
			}
			snapshot := inspectProcessSnapshot(t, process)
			if string(snapshot.JSON()) != string(head.ProcessSnapshots()[0].JSON()) {
				t.Fatal("stopped runtime snapshot does not match acknowledged head")
			}
			if got := dispatcher.calls.Load(); got != test.wantDispatches {
				t.Fatalf("dispatch calls=%d, want %d", got, test.wantDispatches)
			}
			unresolved := runtimeErr.UnresolvedEffectIDs()
			if test.wantUnresolvedID {
				boundaries := recorder.effectBoundaries()
				if len(unresolved) != 1 || len(boundaries) < 2 ||
					unresolved[0] != boundaries[0].Request().ID() {
					t.Fatalf("unresolved=%v boundaries=%v", unresolved, boundaries)
				}
				unresolved[0] = EffectID{}
				if !runtimeErr.UnresolvedEffectIDs()[0].Valid() {
					t.Fatal("caller mutated the retained unresolved identities")
				}
			} else if len(unresolved) != 0 {
				t.Fatalf("undispatched pending failure has unresolved=%v", unresolved)
			}
			if err := process.Pause(t.Context(), "stopped instance"); !errors.Is(err, test.cause) {
				t.Fatalf("stopped control request lost runtime cause: %v", err)
			}
			if err := engine.ReleaseTree(t.Context(), process.ID()); err != nil {
				t.Fatal(err)
			}
			awaitRuntimeError(t, process, test.cause)
			if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTreeCommitterFaultPreservesEveryConcurrentEffectForReconciliation(t *testing.T) {
	recorder := &recordingTreeCommitter{}
	committer := &rejectingEffectDurability{
		recordingTreeCommitter: recorder,
		rejectedKind:           EffectBoundaryKindSettled,
		err:                    errors.New("committer unavailable"),
	}
	dispatcher := newBlockingChildDispatcher("first", "second", "third")
	t.Cleanup(dispatcher.ReleaseAll)
	deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
	engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodePayload(childTestInput{Mode: "wait:all"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	started := make([]string, 0, 3)
	for len(started) < cap(started) {
		select {
		case name := <-dispatcher.started:
			started = append(started, name)
		case <-time.After(2 * time.Second):
			var termination Termination
			if inspectProcessSnapshot(t, root).Status().Terminal() {
				result, _ := root.Await(context.Background())
				termination = result.Termination()
			}
			t.Fatalf(
				"started Dispatcher Effects=%v root_status=%s termination=%+v outcomes=%d boundaries=%d checkpoints=%d",
				started, inspectProcessSnapshot(t, root).Status(), termination, len(recorder.treeCheckpoints()),
				len(recorder.effectBoundaries()), len(recorder.treeCheckpoints()),
			)
		}
	}
	dispatcher.Release(started[0])
	awaitRuntimeError(t, root, committer.err)

	childIDs := directChildIDs(t, engine, root.ID())
	if len(childIDs) != len(started) {
		t.Fatalf("child count=%d, want %d", len(childIDs), len(started))
	}
	for _, encoded := range childIDs {
		childID, parseErr := ParseProcessID(encoded)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		child, exists := engine.Process(childID)
		if !exists {
			t.Fatalf("child %s is missing", childID)
		}
		runtimeErr := awaitRuntimeError(t, child, committer.err)
		unresolved := runtimeErr.UnresolvedEffectIDs()
		if len(unresolved) != 1 {
			t.Fatalf("child %s unresolved=%v", childID, unresolved)
		}
	}

	dispatcher.ReleaseAll()
	closeEngineEventually(t, engine)
}

func TestTreeCommitterFaultReleasesConcurrentChildAdmissionOwnership(t *testing.T) {
	secondKey, err := ParseChildKey("second")
	if err != nil {
		t.Fatal(err)
	}
	admitter := &blockingChildAdmission{
		childKey: secondKey,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	dispatcher := newBlockingChildDispatcher("first", "second", "third")
	t.Cleanup(func() {
		admitter.once.Do(func() { close(admitter.entered) })
		select {
		case <-admitter.release:
		default:
			close(admitter.release)
		}
		dispatcher.ReleaseAll()
	})
	committer := &rejectingEffectDurability{
		recordingTreeCommitter: &recordingTreeCommitter{},
		rejectedKind:           EffectBoundaryKindSettled,
		err:                    errors.New("committer unavailable"),
	}
	deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
	engine, err := NewEngine(EngineConfig{
		TreeCommitter:   committer,
		ProcessAdmitter: admitter,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodePayload(childTestInput{Mode: "wait:all"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	var startedEffect string
	for startedEffect == "" {
		select {
		case startedEffect = <-dispatcher.started:
		case <-time.After(2 * time.Second):
			t.Fatal("first child Dispatcher Effect did not start")
		}
	}
	select {
	case <-admitter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second child admission did not start")
	}

	dispatcher.Release(startedEffect)
	runtimeErr := awaitRuntimeError(t, root, committer.err)
	if len(runtimeErr.UnresolvedEffectIDs()) != 1 {
		t.Fatalf("root unresolved=%v", runtimeErr.UnresolvedEffectIDs())
	}
	before := inspectProcessSnapshot(t, root)
	close(admitter.release)
	dispatcher.ReleaseAll()
	closeEngineEventually(t, engine)
	stopped := root.handle.runtime.Load()
	<-stopped.done
	after := inspectProcessSnapshot(t, root)
	if string(before.JSON()) != string(after.JSON()) {
		t.Fatal("late child admission changed the acknowledged head after a fault")
	}
	parent := stopped.processes[root.ID()]
	_, record := parent.prepared.pendingEffect(runtimeErr.UnresolvedEffectIDs()[0])
	if record == nil || record.Settlement != nil {
		t.Fatal("late child admission replaced pending evidence after a fault")
	}
}

func awaitRuntimeError(t *testing.T, process *Process, cause error) *RuntimeError {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := process.Await(ctx)
	if !errors.Is(err, cause) || result.Valid() || result.ProcessID().Valid() || result.Status() != StatusInvalid {
		t.Fatalf("runtime failure result=%+v error=%v, want cause %v", result, err, cause)
	}
	runtimeErr, ok := errors.AsType[*RuntimeError](err)
	if !ok || runtimeErr.ProcessID() != process.ID() || !runtimeErr.IncarnationID().Valid() || !runtimeErr.HeadDigest().Valid() {
		t.Fatalf("runtime failure identity=%+v", runtimeErr)
	}
	return runtimeErr
}

func closeEngineEventually(t *testing.T, engine *Engine) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if err := engine.Close(context.WithoutCancel(t.Context())); err == nil {
			break
		} else if !errors.Is(err, ErrEngineHasActiveProcesses) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatal("Engine retained work after concurrent committer fault")
		case <-ticker.C:
		}
	}
}

func TestDurableUnknownResolutionCommitsAResolvedBoundary(t *testing.T) {
	committer := &recordingTreeCommitter{}
	dispatcher := &failingEngineTestDispatcher{}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(engineTestInput{Value: "resolve"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := waitForUnknownSettlement(t, process)
	wire, _ := snapshot.wire()
	effectID := wire.Prepared.Effects[0].ID
	payload, _ := json.Marshal(engineTestMessage{Kind: "result", Value: "resolved"})
	settlement, _ := NewSettlement(effectID, SettlementStatusSucceeded, payload)
	if err := process.ResolveUnknownEffect(context.Background(), settlement); err != nil {
		t.Fatal(err)
	}
	if result := awaitResult(t, process); result.Status() != StatusCompleted {
		t.Fatalf("result status=%s", result.Status())
	}
	boundaries := committer.effectBoundaries()
	if len(boundaries) != 3 || boundaries[0].Kind() != EffectBoundaryKindPending ||
		boundaries[1].Kind() != EffectBoundaryKindSettled ||
		boundaries[2].Kind() != EffectBoundaryKindResolved {
		t.Fatalf("Effect boundary order=%v", boundaries)
	}
	pending, present := boundaries[0].Settlement()
	if present || pending.Valid() {
		t.Fatalf("pending settlement=%+v present=%t", pending, present)
	}
	unknown, present := boundaries[1].Settlement()
	if !present || unknown.Status() != SettlementStatusUnknown || unknown.EffectID() != effectID {
		t.Fatalf("unknown settlement=%+v present=%t", unknown, present)
	}
	resolved, present := boundaries[2].Settlement()
	if !present || resolved.Status() != SettlementStatusSucceeded || resolved.EffectID() != effectID {
		t.Fatalf("resolved settlement=%+v present=%t", resolved, present)
	}
}

func TestRestorePendingEffectUsesOneDurableRecoveryDecision(t *testing.T) {
	pending, deployment, effectID := durablePendingTreeSnapshot(t)
	for _, test := range []pendingEffectRecoveryCase{
		{
			name:   "never replay commits Unknown without dispatch",
			policy: ReplayPolicyNever, wantStatus: SettlementStatusUnknown,
		},
		{
			name:   "same identity replays and commits its definite result",
			policy: ReplayPolicySameIdentity, wantCalls: 1,
			wantStatus: SettlementStatusSucceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runPendingEffectRecoveryCase(t, pending, deployment, effectID, test)
		})
	}
}

type pendingEffectRecoveryCase struct {
	name       string
	policy     ReplayPolicy
	wantCalls  int32
	wantStatus SettlementStatus
}

func runPendingEffectRecoveryCase(
	t *testing.T,
	pending TreeSnapshot,
	deployment Deployment,
	effectID EffectID,
	test pendingEffectRecoveryCase,
) {
	t.Helper()
	committer := &recordingTreeCommitter{}
	dispatcher := &engineTestDispatcher{policy: test.policy}
	restoredDeployment := engineTestDeployment(t, deployment.Definition(), dispatcher)
	engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.RestoreTree(context.Background(), restoredDeployment, pending)
	if err != nil {
		t.Fatal(err)
	}
	assertRecoveredProcess(t, process, effectID, test.wantStatus)
	if got := dispatcher.calls.Load(); got != test.wantCalls {
		t.Fatalf("recovery dispatch calls=%d, want %d", got, test.wantCalls)
	}
	assertRecoveryBoundary(t, committer, effectID, test.wantStatus)
	if test.wantStatus == SettlementStatusUnknown {
		_ = process.Kill(context.Background(), "test cleanup")
		_ = awaitResult(t, process)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func assertRecoveredProcess(t *testing.T, process *Process, effectID EffectID, want SettlementStatus) {
	t.Helper()
	if want != SettlementStatusUnknown {
		if result := awaitResult(t, process); result.Status() != StatusCompleted {
			t.Fatalf("restored result status=%s", result.Status())
		}
		return
	}
	snapshot := waitForUnknownSettlement(t, process)
	wire, _ := snapshot.wire()
	if wire.Prepared.Effects[0].ID != effectID {
		t.Fatalf("restored EffectID=%s, want %s", wire.Prepared.Effects[0].ID, effectID)
	}
}

func assertRecoveryBoundary(
	t *testing.T,
	committer *recordingTreeCommitter,
	effectID EffectID,
	wantStatus SettlementStatus,
) {
	t.Helper()
	boundaries := committer.effectBoundaries()
	if len(boundaries) == 0 {
		t.Fatal("recovery settlement boundary is missing")
	}
	boundary := boundaries[0]
	settlement, present := boundary.Settlement()
	if boundary.Kind() != EffectBoundaryKindSettled || !present ||
		boundary.Request().ID() != effectID || settlement.EffectID() != effectID ||
		settlement.Status() != wantStatus {
		t.Fatalf("recovery boundary=%+v settlement=%+v present=%t", boundary, settlement, present)
	}
	activations := committer.treeActivations()
	if len(activations) != 1 || boundary.PreviousTreeDigest() != activations[0].TreeSnapshot().Digest() {
		t.Fatalf("activation=%v previous head=%s", activations, boundary.PreviousTreeDigest())
	}
}

func TestRestorePendingEffectRejectsInvalidReplayPolicyBeforeActivation(t *testing.T) {
	pending, deployment, _ := durablePendingTreeSnapshot(t)
	committer := &recordingTreeCommitter{}
	restoredDeployment := engineTestDeployment(
		t, deployment.Definition(), &engineTestDispatcher{policy: ReplayPolicyInvalid},
	)
	engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RestoreTree(
		context.Background(), restoredDeployment, pending,
	); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("invalid replay policy restore error=%v", err)
	}
	if got := len(committer.treeActivations()); got != 0 {
		t.Fatalf("invalid replay policy reached activation; count=%d", got)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func durablePendingTreeSnapshot(
	t *testing.T,
) (TreeSnapshot, Deployment, EffectID) {
	t.Helper()
	committer := &recordingTreeCommitter{}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(
		t, definition, &engineTestDispatcher{policy: ReplayPolicyNever},
	)
	engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(engineTestInput{Value: "pending recovery"})
	if _, err := engine.Run(context.Background(), deployment, input); err != nil {
		t.Fatal(err)
	}
	boundaries := committer.effectBoundaries()
	if len(boundaries) == 0 || boundaries[0].Kind() != EffectBoundaryKindPending {
		t.Fatalf("pending boundary is missing: %v", boundaries)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	return boundaries[0].TreeSnapshot(), deployment, boundaries[0].Request().ID()
}

func TestKillPreservesUnknownEffectIdentityInTermination(t *testing.T) {
	dispatcher := &failingEngineTestDispatcher{}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, _ := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	input, _ := EncodePayload(engineTestInput{Value: "unknown"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := waitForUnknownSettlement(t, process)
	wire, _ := snapshot.wire()
	want := wire.Prepared.Effects[0].ID
	if err := process.Kill(context.Background(), "operator reconciliation"); err != nil {
		t.Fatal(err)
	}
	result := awaitResult(t, process)
	unresolved := result.Termination().UnresolvedEffectIDs()
	if len(unresolved) != 1 || unresolved[0] != want {
		t.Fatalf("unresolved=%v, want %s", unresolved, want)
	}
}

func TestCaptureReturnsAcknowledgedHead(t *testing.T) {
	committer := NewMemoryTreeCommitter()
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, _ := NewEngine(EngineConfig{TreeCommitter: committer})
	input, _ := EncodePayload(engineTestInput{Value: "capture"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusWaiting)
	snapshot, err := engine.CaptureTree(context.Background(), process.ID())
	if err != nil || !snapshot.Valid() {
		t.Fatalf("CaptureTree valid=%v error=%v", snapshot.Valid(), err)
	}
	head, exists, loadErr := committer.LoadTree(t.Context(), process.ID())
	if loadErr != nil || !exists || head.Digest() != snapshot.Digest() {
		t.Fatalf("capture differs from acknowledged head: exists=%t error=%v", exists, loadErr)
	}
	_ = process.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, process)
}

func TestEngineCloseRejectsUnpublishedTerminalCheckpoint(t *testing.T) {
	committer := &blockingTerminalCheckpointDurability{
		recordingTreeCommitter: &recordingTreeCommitter{},
		entered:                make(chan struct{}),
		release:                make(chan struct{}),
	}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(
		t, definition, &engineTestDispatcher{policy: ReplayPolicyNever},
	)
	engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(engineTestInput{Value: "terminal checkpoint"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-committer.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal checkpoint did not start")
	}
	if inspectProcessSnapshot(t, process).Status() != StatusRunning {
		t.Fatalf("Process status=%s before terminal checkpoint", inspectProcessSnapshot(t, process).Status())
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); !errors.Is(err, ErrEngineHasActiveProcesses) {
		t.Fatalf("Close during terminal checkpoint error=%v", err)
	}
	close(committer.release)
	if result := awaitResult(t, process); result.Status() != StatusCompleted {
		t.Fatalf("result status=%s", result.Status())
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestDurableObservationsCarryCurrentIncarnation(t *testing.T) {
	const observationBufferCapacity = 32
	committer := &recordingTreeCommitter{}
	events := make(chan Event, observationBufferCapacity)
	deltas := make(chan Delta, observationBufferCapacity)
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, err := NewEngine(EngineConfig{
		TreeCommitter: committer,
		EventListeners: []EventListener{EventListenerFunc(func(_ context.Context, event Event) {
			events <- event
		})},
		DeltaListeners: []DeltaListener{DeltaListenerFunc(func(_ context.Context, delta Delta) {
			deltas <- delta
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(engineTestInput{Value: "observation"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err := engine.FlushDeltas(context.Background()); err != nil {
		t.Fatal(err)
	}
	tree := committer.treeCheckpoints()[0].TreeSnapshot()
	want := tree.IncarnationID()
	for _, boundary := range committer.effectBoundaries() {
		if got, ok := boundary.Request().TreeIncarnationID(); !ok || got != want {
			t.Fatalf("EffectRequest incarnation=%s present=%t, want %s", got, ok, want)
		}
		boundary.request.incarnationID = TreeIncarnationID{}
		if boundary.Valid() {
			t.Fatal("durable boundary accepted a request without its writer identity")
		}
	}
	if len(events) == 0 || len(deltas) == 0 {
		t.Fatalf("events=%d deltas=%d", len(events), len(deltas))
	}
	for len(events) > 0 {
		event := <-events
		if got, ok := event.TreeIncarnationID(); !ok || got != want {
			t.Fatalf("Event incarnation=%s present=%t, want %s", got, ok, want)
		}
	}
	for len(deltas) > 0 {
		delta := <-deltas
		if got, ok := delta.TreeIncarnationID(); !ok || got != want {
			t.Fatalf("Delta incarnation=%s present=%t, want %s", got, ok, want)
		}
	}
}

type rejectingStartCheckpointDurability struct {
	*recordingTreeCommitter
	err error
}

func (r *rejectingStartCheckpointDurability) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	if err := r.recordingTreeCommitter.CommitCheckpoint(ctx, checkpoint); err != nil {
		return err
	}
	if checkpoint.Kind() == TreeCheckpointKindStart {
		return r.err
	}
	return nil
}

func TestDurableStartSeparatesInitializationAcceptanceFromCheckpoint(t *testing.T) {
	initializationErr := errors.New("initialization failed")
	acknowledgmentErr := errors.New("initialization rejected")
	persistenceErr := errors.New("checkpoint unavailable")
	for _, test := range []struct {
		name              string
		initializationErr error
		acknowledgmentErr error
		persistenceErr    error
		wantError         error
		wantStatus        ProcessInitializationOutcomeStatus
		wantCheckpoints   int
	}{
		{name: "initialization failure", initializationErr: initializationErr, wantError: initializationErr, wantStatus: ProcessInitializationOutcomeStatusFailed},
		{name: "initialization rejected", acknowledgmentErr: acknowledgmentErr, wantError: acknowledgmentErr, wantStatus: ProcessInitializationOutcomeStatusInitialized},
		{name: "persistence failure after initialization acceptance", persistenceErr: persistenceErr, wantError: persistenceErr, wantStatus: ProcessInitializationOutcomeStatusInitialized, wantCheckpoints: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingTreeCommitter{}
			committer := &rejectingStartCheckpointDurability{recordingTreeCommitter: recorder, err: test.persistenceErr}
			var outcomes []ProcessInitializationOutcome
			engine, err := NewEngine(EngineConfig{
				TreeCommitter: committer,
				ProcessInitializationOutcomeAcknowledger: ProcessInitializationOutcomeAcknowledgerFunc(func(_ context.Context, outcome ProcessInitializationOutcome) error {
					if len(recorder.treeCheckpoints()) != 0 {
						t.Error("persistence preceded initialization acceptance")
					}
					outcomes = append(outcomes, outcome)
					return test.acknowledgmentErr
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			deployment := newChildTestDeployment(t)
			if test.initializationErr != nil {
				deployment = failingStartDeployment(t, test.initializationErr)
			}
			input, err := EncodePayload(childTestInput{Mode: "leaf"})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(t.Context(), deployment, input)
			if process != nil || !errors.Is(err, test.wantError) {
				t.Fatalf("Start process=%v error=%v", process, err)
			}
			if len(outcomes) != 1 || outcomes[0].Status() != test.wantStatus || !outcomes[0].Valid() {
				t.Fatalf("initialization outcomes=%v, want one %s", outcomes, test.wantStatus)
			}
			checkpoints := recorder.treeCheckpoints()
			if len(checkpoints) != test.wantCheckpoints {
				t.Fatalf("checkpoints=%d, want %d", len(checkpoints), test.wantCheckpoints)
			}
			if len(checkpoints) == 1 && (checkpoints[0].Kind() != TreeCheckpointKindStart || !checkpoints[0].Valid() || checkpoints[0].PreviousTreeDigest() != (Digest{})) {
				t.Fatalf("invalid initial checkpoint: %+v", checkpoints[0])
			}
			assertNoPendingProcessStarts(t, engine)
			if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
		})
	}
}
