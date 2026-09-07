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

type recordingTreeDurability struct {
	mu          sync.Mutex
	outcomes    []ProcessStartOutcome
	activations []TreeActivation
	effects     []EffectBoundary
	checkpoints []TreeCheckpoint
	pending     atomic.Bool
}

func (r *recordingTreeDurability) AcknowledgeProcessStartOutcome(
	_ context.Context,
	outcome ProcessStartOutcome,
) error {
	r.mu.Lock()
	r.outcomes = append(r.outcomes, outcome)
	r.mu.Unlock()
	return nil
}

func (r *recordingTreeDurability) ActivateTree(
	_ context.Context,
	activation TreeActivation,
) error {
	r.mu.Lock()
	r.activations = append(r.activations, activation)
	r.mu.Unlock()
	return nil
}

func (r *recordingTreeDurability) CommitEffect(
	_ context.Context,
	boundary EffectBoundary,
) error {
	r.mu.Lock()
	r.effects = append(r.effects, boundary)
	r.mu.Unlock()
	if boundary.Kind() == EffectBoundaryPending {
		r.pending.Store(true)
	}
	return nil
}

func (r *recordingTreeDurability) CommitCheckpoint(
	_ context.Context,
	checkpoint TreeCheckpoint,
) error {
	r.mu.Lock()
	r.checkpoints = append(r.checkpoints, checkpoint)
	r.mu.Unlock()
	return nil
}

func (r *recordingTreeDurability) effectBoundaries() []EffectBoundary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]EffectBoundary(nil), r.effects...)
}

func (r *recordingTreeDurability) startOutcomes() []ProcessStartOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ProcessStartOutcome(nil), r.outcomes...)
}

func (r *recordingTreeDurability) treeCheckpoints() []TreeCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]TreeCheckpoint(nil), r.checkpoints...)
}

func (r *recordingTreeDurability) treeActivations() []TreeActivation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]TreeActivation(nil), r.activations...)
}

type rejectingEffectDurability struct {
	*recordingTreeDurability
	rejectedKind EffectBoundaryKind
	err          error
}

type blockingTerminalCheckpointDurability struct {
	*recordingTreeDurability
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
	if checkpoint.Kind() == TreeCheckpointTerminal {
		b.once.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.recordingTreeDurability.CommitCheckpoint(ctx, checkpoint)
}

type typedNilTreeDurability struct{}

func (*typedNilTreeDurability) AcknowledgeProcessStartOutcome(
	context.Context,
	ProcessStartOutcome,
) error {
	return nil
}

func (*typedNilTreeDurability) ActivateTree(context.Context, TreeActivation) error {
	return nil
}

func (*typedNilTreeDurability) CommitEffect(context.Context, EffectBoundary) error {
	return nil
}

func (*typedNilTreeDurability) CommitCheckpoint(context.Context, TreeCheckpoint) error {
	return nil
}

func TestTreeDurabilityConfigurationIsUnambiguous(t *testing.T) {
	var typedNil *typedNilTreeDurability
	if _, err := NewEngine(EngineConfig{TreeDurability: typedNil}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("typed-nil TreeDurability error=%v", err)
	}
	durability := &recordingTreeDurability{}
	acknowledger := ProcessStartOutcomeAcknowledgerFunc(func(
		context.Context,
		ProcessStartOutcome,
	) error {
		return nil
	})
	if _, err := NewEngine(EngineConfig{
		TreeDurability: durability, ProcessStartOutcomeAcknowledger: acknowledger,
	}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("dual outcome owners error=%v", err)
	}
}

func TestRestoreTreeRejectsDurabilityModeMismatch(t *testing.T) {
	ephemeral := completedTreeSnapshot(t)
	durability := &recordingTreeDurability{}
	durableEngine, _ := NewEngine(EngineConfig{TreeDurability: durability})
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	if _, err := durableEngine.RestoreTree(context.Background(), deployment, ephemeral); !errors.Is(err, ErrTreeDurabilityMismatch) {
		t.Fatalf("ephemeral-to-durable restore error=%v", err)
	}

	runningEngine, _ := NewEngine(EngineConfig{TreeDurability: durability})
	input, _ := EncodeInput(engineTestInput{Value: "durable"})
	result, err := runningEngine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	var durableSnapshot TreeSnapshot
	for _, checkpoint := range durability.treeCheckpoints() {
		if checkpoint.TreeSnapshot().RootID() == result.ProcessID() {
			durableSnapshot = checkpoint.TreeSnapshot()
		}
	}
	if !durableSnapshot.Valid() {
		t.Fatal("durable terminal snapshot is missing")
	}
	ephemeralEngine, _ := NewEngine(EngineConfig{})
	if _, err := ephemeralEngine.RestoreTree(
		context.Background(), deployment, durableSnapshot,
	); !errors.Is(err, ErrTreeDurabilityMismatch) {
		t.Fatalf("durable-to-ephemeral restore error=%v", err)
	}
}

func TestRestoreTreeRejectsLocalRegistrationBeforeActivation(t *testing.T) {
	durability := &recordingTreeDurability{}
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(
		t, definition, &engineTestDispatcher{policy: ReplayPolicyNever},
	)
	source, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "restore reservation"})
	original, err := source.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, original, StatusWaiting)
	parked := waitForDurableCheckpoint(t, durability, original.ID(), TreeCheckpointParked)

	destination, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := destination.RestoreTree(context.Background(), deployment, parked)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(durability.treeActivations()); got != 1 {
		t.Fatalf("activation count=%d, want 1", got)
	}
	if _, err := destination.RestoreTree(
		context.Background(), deployment, parked,
	); !errors.Is(err, ErrProcessAlreadyExists) {
		t.Fatalf("duplicate RestoreTree error=%v", err)
	}
	if got := len(durability.treeActivations()); got != 1 {
		t.Fatalf("duplicate restore reached activation; count=%d", got)
	}

	_ = restored.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, restored)
	_ = original.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, original)
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitForDurableCheckpoint(
	t *testing.T,
	durability *recordingTreeDurability,
	rootID ProcessID,
	kind TreeCheckpointKind,
) TreeSnapshot {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		for _, checkpoint := range durability.treeCheckpoints() {
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
	if err := r.recordingTreeDurability.CommitEffect(ctx, boundary); err != nil {
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
			name: "pending is definitely undispatched", kind: EffectBoundaryPending,
			cause:           errors.New("durability unavailable"),
			wantFailureKind: FailureKindExternal, wantFailureCode: treeDurabilityFailureCode,
		},
		{
			name: "settled preserves ambiguous Effect identity", kind: EffectBoundarySettled,
			cause: errors.New("durability unavailable"), wantDispatches: 1,
			wantUnresolvedID: true, wantFailureKind: FailureKindExternal,
			wantFailureCode: treeDurabilityFailureCode,
		},
		{
			name: "content conflict is a Host contract violation", kind: EffectBoundaryPending,
			cause: ErrDurabilityConflict, wantFailureKind: FailureKindContract,
			wantFailureCode: treeDurabilityConflictCode,
		},
		{
			name: "stale writer is fenced", kind: EffectBoundaryPending,
			cause: ErrTreeIncarnationConflict, wantFailureKind: FailureKindExternal,
			wantFailureCode: treeIncarnationConflictCode,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingTreeDurability{}
			durability := &rejectingEffectDurability{
				recordingTreeDurability: recorder, rejectedKind: test.kind, err: test.cause,
			}
			dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
			definition := newEngineTestDefinition(t, "engine.effect", "effect")
			deployment := engineTestDeployment(t, definition, dispatcher)
			listener := &recordingEventListener{}
			engine, err := NewEngine(EngineConfig{TreeDurability: durability, EventListeners: []EventListener{listener}})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := EncodeInput(engineTestInput{Value: "durability"})
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
			head, _ := recorder.startOutcomes()[0].TreeSnapshot()
			if test.kind == EffectBoundarySettled {
				head = recorder.effectBoundaries()[0].TreeSnapshot()
			}
			if runtimeErr.HeadDigest() != head.Digest() || process.Status() != StatusRunning {
				t.Fatalf("runtime failure changed acknowledged state: digest=%s status=%s", runtimeErr.HeadDigest(), process.Status())
			}
			snapshot, err := process.Snapshot(t.Context())
			if err != nil || string(snapshot.JSON()) != string(head.ProcessSnapshots()[0].JSON()) {
				t.Fatalf("stopped runtime snapshot does not match acknowledged head: %v", err)
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
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTreeDurabilityFaultPreservesEveryConcurrentEffectForReconciliation(t *testing.T) {
	recorder := &recordingTreeDurability{}
	durability := &rejectingEffectDurability{
		recordingTreeDurability: recorder,
		rejectedKind:            EffectBoundarySettled,
		err:                     errors.New("durability unavailable"),
	}
	dispatcher := newBlockingChildDispatcher("first", "second", "third")
	t.Cleanup(dispatcher.ReleaseAll)
	deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodeInput(childTestInput{Mode: "wait:all"})
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
			if root.Status().Terminal() {
				result, _ := root.Await(context.Background())
				termination = result.Termination()
			}
			t.Fatalf(
				"started Dispatcher Effects=%v root_status=%s termination=%+v outcomes=%d boundaries=%d checkpoints=%d",
				started, root.Status(), termination, len(recorder.startOutcomes()),
				len(recorder.effectBoundaries()), len(recorder.treeCheckpoints()),
			)
		}
	}
	dispatcher.Release(started[0])
	awaitRuntimeError(t, root, durability.err)

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
		runtimeErr := awaitRuntimeError(t, child, durability.err)
		unresolved := runtimeErr.UnresolvedEffectIDs()
		if len(unresolved) != 1 {
			t.Fatalf("child %s unresolved=%v", childID, unresolved)
		}
	}

	dispatcher.ReleaseAll()
	closeEngineEventually(t, engine)
}

func TestTreeDurabilityFaultReleasesConcurrentChildAdmissionOwnership(t *testing.T) {
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
	durability := &rejectingEffectDurability{
		recordingTreeDurability: &recordingTreeDurability{},
		rejectedKind:            EffectBoundarySettled,
		err:                     errors.New("durability unavailable"),
	}
	deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
	engine, err := NewEngine(EngineConfig{
		TreeDurability:  durability,
		ProcessAdmitter: admitter,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodeInput(childTestInput{Mode: "wait:all"})
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
	runtimeErr := awaitRuntimeError(t, root, durability.err)
	if len(runtimeErr.UnresolvedEffectIDs()) != 1 {
		t.Fatalf("root unresolved=%v", runtimeErr.UnresolvedEffectIDs())
	}
	close(admitter.release)
	dispatcher.ReleaseAll()
	closeEngineEventually(t, engine)
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
		if err := engine.Close(); err == nil {
			break
		} else if !errors.Is(err, ErrEngineHasActiveProcesses) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatal("Engine retained work after concurrent durability fault")
		case <-ticker.C:
		}
	}
}

func TestDurableUnknownResolutionCommitsAResolvedBoundary(t *testing.T) {
	durability := &recordingTreeDurability{}
	dispatcher := &failingEngineTestDispatcher{}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "resolve"})
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
	boundaries := durability.effectBoundaries()
	if len(boundaries) != 3 || boundaries[0].Kind() != EffectBoundaryPending ||
		boundaries[1].Kind() != EffectBoundarySettled ||
		boundaries[2].Kind() != EffectBoundaryResolved {
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
	durability := &recordingTreeDurability{}
	dispatcher := &engineTestDispatcher{policy: test.policy}
	restoredDeployment := engineTestDeployment(t, deployment.Definition(), dispatcher)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
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
	assertRecoveryBoundary(t, durability, effectID, test.wantStatus)
	if test.wantStatus == SettlementStatusUnknown {
		_ = process.Kill(context.Background(), "test cleanup")
		_ = awaitResult(t, process)
	}
	if err := engine.Close(); err != nil {
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
	durability *recordingTreeDurability,
	effectID EffectID,
	wantStatus SettlementStatus,
) {
	t.Helper()
	boundaries := durability.effectBoundaries()
	if len(boundaries) == 0 {
		t.Fatal("recovery settlement boundary is missing")
	}
	boundary := boundaries[0]
	settlement, present := boundary.Settlement()
	if boundary.Kind() != EffectBoundarySettled || !present ||
		boundary.Request().ID() != effectID || settlement.EffectID() != effectID ||
		settlement.Status() != wantStatus {
		t.Fatalf("recovery boundary=%+v settlement=%+v present=%t", boundary, settlement, present)
	}
	activations := durability.treeActivations()
	if len(activations) != 1 || boundary.PreviousTreeDigest() != activations[0].TreeSnapshot().Digest() {
		t.Fatalf("activation=%v previous head=%s", activations, boundary.PreviousTreeDigest())
	}
}

func TestRestorePendingEffectRejectsInvalidReplayPolicyBeforeActivation(t *testing.T) {
	pending, deployment, _ := durablePendingTreeSnapshot(t)
	durability := &recordingTreeDurability{}
	restoredDeployment := engineTestDeployment(
		t, deployment.Definition(), &engineTestDispatcher{policy: ReplayPolicyInvalid},
	)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RestoreTree(
		context.Background(), restoredDeployment, pending,
	); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("invalid replay policy restore error=%v", err)
	}
	if got := len(durability.treeActivations()); got != 0 {
		t.Fatalf("invalid replay policy reached activation; count=%d", got)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func durablePendingTreeSnapshot(
	t *testing.T,
) (TreeSnapshot, Deployment, EffectID) {
	t.Helper()
	durability := &recordingTreeDurability{}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(
		t, definition, &engineTestDispatcher{policy: ReplayPolicyNever},
	)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "pending recovery"})
	if _, err := engine.Run(context.Background(), deployment, input); err != nil {
		t.Fatal(err)
	}
	boundaries := durability.effectBoundaries()
	if len(boundaries) == 0 || boundaries[0].Kind() != EffectBoundaryPending {
		t.Fatalf("pending boundary is missing: %v", boundaries)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	return boundaries[0].TreeSnapshot(), deployment, boundaries[0].Request().ID()
}

func TestKillPreservesUnknownEffectIdentityInTermination(t *testing.T) {
	dispatcher := &failingEngineTestDispatcher{}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(engineTestInput{Value: "unknown"})
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

func TestDurableTreeRejectsCallerDrivenCapture(t *testing.T) {
	durability := &recordingTreeDurability{}
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, _ := NewEngine(EngineConfig{TreeDurability: durability})
	input, _ := EncodeInput(engineTestInput{Value: "capture"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.CaptureTree(context.Background(), process.ID()); !errors.Is(err, ErrTreeCaptureUnavailable) {
		t.Fatalf("CaptureTree error=%v", err)
	}
	_ = process.Kill(context.Background(), "test cleanup")
	_ = awaitResult(t, process)
}

func TestEngineCloseRejectsUnpublishedTerminalCheckpoint(t *testing.T) {
	durability := &blockingTerminalCheckpointDurability{
		recordingTreeDurability: &recordingTreeDurability{},
		entered:                 make(chan struct{}),
		release:                 make(chan struct{}),
	}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(
		t, definition, &engineTestDispatcher{policy: ReplayPolicyNever},
	)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "terminal checkpoint"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-durability.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal checkpoint did not start")
	}
	if process.Status() != StatusRunning {
		t.Fatalf("Process status=%s before terminal checkpoint", process.Status())
	}
	if err := engine.Close(); !errors.Is(err, ErrEngineHasActiveProcesses) {
		t.Fatalf("Close during terminal checkpoint error=%v", err)
	}
	close(durability.release)
	if result := awaitResult(t, process); result.Status() != StatusCompleted {
		t.Fatalf("result status=%s", result.Status())
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDurableObservationsCarryCurrentIncarnation(t *testing.T) {
	const observationBufferCapacity = 32
	durability := &recordingTreeDurability{}
	events := make(chan Event, observationBufferCapacity)
	deltas := make(chan Delta, observationBufferCapacity)
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, dispatcher)
	engine, err := NewEngine(EngineConfig{
		TreeDurability: durability,
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
	input, _ := EncodeInput(engineTestInput{Value: "observation"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err := engine.FlushDeltas(context.Background()); err != nil {
		t.Fatal(err)
	}
	outcomes := durability.startOutcomes()
	tree, present := outcomes[0].TreeSnapshot()
	if !present {
		t.Fatal("durable root outcome has no tree")
	}
	want, _ := tree.IncarnationID()
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
