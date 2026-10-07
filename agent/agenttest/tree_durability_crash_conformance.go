package agenttest

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync"
	"testing"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
)

type crashCommitKind uint8

const (
	crashCommitInvalid crashCommitKind = iota
	crashCommitRootStart
	crashCommitActivation
	crashCommitEffectPending
	crashCommitEffectSettled
	crashCommitEffectResolved
	crashCommitChildSignal
	crashCommitChildCancel
	crashCommitCheckpointChild
	crashCommitCheckpointInput
	crashCommitCheckpointProgress
	crashCommitCheckpointParked
	crashCommitCheckpointCancellation
	crashCommitCheckpointTerminal
)

// childControlCommitKind names the settled commit a child control produces;
// any other settled Effect is an ordinary settlement.
func childControlCommitKind(operation childControlOperation) crashCommitKind {
	switch operation {
	case childControlSignal:
		return crashCommitChildSignal
	case childControlCancel:
		return crashCommitChildCancel
	default:
		return crashCommitEffectSettled
	}
}

func effectCommitKind(boundary agent.EffectBoundary) crashCommitKind {
	switch boundary.Kind() {
	case agent.EffectBoundaryKindPending:
		return crashCommitEffectPending
	case agent.EffectBoundaryKindResolved:
		return crashCommitEffectResolved
	case agent.EffectBoundaryKindSettled:
		return childControlCommitKind(childControlOperationFor(boundary.Request().Effect()))
	default:
		return crashCommitInvalid
	}
}

func checkpointCommitKind(checkpoint agent.TreeCheckpoint) crashCommitKind {
	switch checkpoint.Kind() {
	case agent.TreeCheckpointKindStart:
		return crashCommitRootStart
	case agent.TreeCheckpointKindChildStart:
		return crashCommitCheckpointChild
	case agent.TreeCheckpointKindSignals:
		return crashCommitCheckpointInput
	case agent.TreeCheckpointKindProgress:
		return crashCommitCheckpointProgress
	case agent.TreeCheckpointKindParked:
		return crashCommitCheckpointParked
	case agent.TreeCheckpointKindTerminal:
		return crashCommitCheckpointTerminal
	default:
		return crashCommitInvalid
	}
}

type crashCommitPhase uint8

const (
	crashCommitPhaseInvalid crashCommitPhase = iota
	crashCommitBefore
	crashCommitAfter
)

type crashCommitPoint struct {
	kind  crashCommitKind
	phase crashCommitPhase
}

func (c crashCommitPoint) valid() bool {
	return c.kind > crashCommitInvalid && c.kind <= crashCommitCheckpointTerminal &&
		(c.phase == crashCommitBefore || c.phase == crashCommitAfter)
}

type crashCommitObservation struct {
	ctx            context.Context
	phase          crashCommitPhase
	previousDigest agent.Digest
	prospective    agent.TreeSnapshot
	boundary       agent.EffectBoundary
}

func (c crashCommitObservation) rootID() agent.ProcessID { return c.prospective.RootID() }

// durableDigest is the only head storage may hold at the cut: the base before
// the commit, or the prospective head once the delegate has installed it.
func (c crashCommitObservation) durableDigest() agent.Digest {
	if c.phase == crashCommitAfter {
		return c.prospective.Digest()
	}
	return c.previousDigest
}

var errSimulatedHostCrash = errors.New("agenttest: simulated host crash")

// treeCommitterCommitGate cuts the callback itself, not an Engine goroutine.
// An after cut occurs after the delegate has advanced its authoritative head
// and before the callback can return to the Runtime for in-memory apply.
type treeCommitterCommitGate struct {
	delegate agent.TreeCommitter
	point    crashCommitPoint

	mu      sync.Mutex
	claimed bool

	reached  chan crashCommitObservation
	decision chan error
	resolve  sync.Once
}

func newTreeCommitterCommitGate(
	t *testing.T,
	delegate agent.TreeCommitter,
	point crashCommitPoint,
) *treeCommitterCommitGate {
	t.Helper()
	if lo.IsNil(delegate) || !point.valid() {
		t.Fatal("invalid tree committer crash gate")
	}
	gate := &treeCommitterCommitGate{
		delegate: delegate,
		point:    point,
		reached:  make(chan crashCommitObservation, 1),
		decision: make(chan error, 1),
	}
	t.Cleanup(func() { gate.abort() })
	return gate
}

func (t *treeCommitterCommitGate) ActivateTree(
	ctx context.Context,
	activation agent.TreeActivation,
) error {
	observation := crashCommitObservation{
		ctx:            ctx,
		previousDigest: activation.PreviousTreeDigest(),
		prospective:    activation.TreeSnapshot(),
	}
	return t.around(crashCommitActivation, observation, func() error {
		return t.delegate.ActivateTree(ctx, activation)
	})
}

func (t *treeCommitterCommitGate) CommitEffect(
	ctx context.Context,
	boundary agent.EffectBoundary,
) error {
	observation := crashCommitObservation{
		ctx:            ctx,
		previousDigest: boundary.PreviousTreeDigest(),
		prospective:    boundary.TreeSnapshot(),
		boundary:       boundary,
	}
	return t.around(effectCommitKind(boundary), observation, func() error {
		return t.delegate.CommitEffect(ctx, boundary)
	})
}

func (t *treeCommitterCommitGate) CommitCheckpoint(
	ctx context.Context,
	checkpoint agent.TreeCheckpoint,
) error {
	kind := checkpointCommitKind(checkpoint)
	if t.point.kind == crashCommitCheckpointCancellation && recordsCancellation(checkpoint.TreeSnapshot()) {
		kind = crashCommitCheckpointCancellation
	}
	observation := crashCommitObservation{
		ctx:            ctx,
		previousDigest: checkpoint.PreviousTreeDigest(),
		prospective:    checkpoint.TreeSnapshot(),
	}
	return t.around(kind, observation, func() error {
		return t.delegate.CommitCheckpoint(ctx, checkpoint)
	})
}

func recordsCancellation(snapshot agent.TreeSnapshot) bool {
	for _, process := range snapshot.ProcessSnapshots() {
		if process.Status() == agent.StatusCanceled {
			return true
		}
	}
	return false
}

func (t *treeCommitterCommitGate) around(
	kind crashCommitKind,
	observation crashCommitObservation,
	commit func() error,
) error {
	if kind != t.point.kind {
		return commit()
	}
	observation.phase = t.point.phase
	if t.point.phase == crashCommitBefore && t.claim() {
		if err := t.cut(observation); err != nil {
			return err
		}
	}
	if err := commit(); err != nil {
		return err
	}
	if t.point.phase == crashCommitAfter && t.claim() {
		return t.cut(observation)
	}
	return nil
}

func (t *treeCommitterCommitGate) claim() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.claimed {
		return false
	}
	t.claimed = true
	return true
}

func (t *treeCommitterCommitGate) cut(observation crashCommitObservation) error {
	t.reached <- observation
	return <-t.decision
}

func (t *treeCommitterCommitGate) await(test *testing.T) crashCommitObservation {
	test.Helper()
	return awaitConformanceValue(test, t.reached, "committer gate was not reached")
}

func (t *treeCommitterCommitGate) continueCommit() {
	t.resolve.Do(func() { t.decision <- nil })
}

func (t *treeCommitterCommitGate) abort() {
	t.resolve.Do(func() { t.decision <- errSimulatedHostCrash })
}

type crashProcessResult struct {
	process *agent.Process
	err     error
}

type crashAwaitResult struct {
	result agent.Result
	err    error
}

const (
	crashDeploymentName        = "agenttest.committer_crash"
	crashDeploymentDescription = "Exercises exact durable crash prefixes."
	crashImplementationSeed    = "agenttest committer crash implementation"
	crashConfigurationSeed     = "agenttest committer crash configuration"
	crashInputValue            = "crash-prefix"
	crashCleanupReason         = "committer crash matrix cleanup"
)

func runTreeCommitterCrashConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	t.Helper()
	tests := []struct {
		name string
		run  func(*testing.T, TreeCommitterConformanceDriver)
	}{
		{name: "root start before commit", run: runCrashBeforeRootStartCommit},
		{name: "root start after commit before Process publication", run: runCrashAfterRootStartCommit},
		{name: "pending before commit", run: runCrashBeforePendingCommit},
		{name: "pending after commit before dispatch", run: runCrashAfterPendingCommit},
		{name: "after dispatch before settled commit", run: runCrashBeforeSettledCommit},
		{name: "settled after commit before memory apply", run: runCrashAfterSettledCommit},
		{name: "resolved before commit", run: runCrashBeforeResolvedCommit},
		{name: "resolved after commit before acknowledgment", run: runCrashAfterResolvedCommit},
		{name: "child before commit", run: runCrashBeforeChildCommit},
		{name: "child after commit before publication", run: runCrashAfterChildCommit},
		{name: "progress before commit", run: runCrashBeforeProgressCommit},
		{name: "progress after commit", run: runCrashAfterProgressCommit},
		{name: "parked after commit before Event publication", run: runCrashAfterParkedCommit},
		{name: "terminal after commit before Result publication", run: runCrashAfterTerminalCommit},
		{name: "activation after CAS before Process publication", run: runCrashAfterActivationCommit},
		{name: "subtree cancellation after commit before publication", run: runCrashAfterSubtreeCancellationCheckpoint},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { test.run(t, factory()) })
	}
}

func runCrashBeforeRootStartCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitRootStart, phase: crashCommitBefore,
	})
	deployment, _ := newCrashDeployment(t, conformanceModePause, agent.ReplayPolicyNever)
	engine := newCrashEngine(t, gate, nil)
	started := startCrashProcessAsync(t, engine, deployment)
	observation := gate.await(t)
	assertCrashHeadAbsent(t, store, observation.rootID())
	if _, found := engine.Process(observation.rootID()); found {
		t.Fatal("root Process published before its base head commit")
	}
	gate.abort()
	result := awaitConformanceValue(t, started, "Engine.Start did not return")
	if !errors.Is(result.err, errSimulatedHostCrash) || result.process != nil {
		t.Fatalf("Start result Process=%v error=%v", result.process != nil, result.err)
	}
	closeCrashEngine(t, engine)
}

func runCrashAfterRootStartCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitRootStart, phase: crashCommitAfter,
	})
	deployment, _ := newCrashDeployment(t, conformanceModePause, agent.ReplayPolicyNever)
	engine := newCrashEngine(t, gate, nil)
	started := startCrashProcessAsync(t, engine, deployment)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	if _, found := engine.Process(observation.rootID()); found {
		t.Fatal("root Process published before its committed base callback returned")
	}

	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	waitForConformanceStatus(t, restoredEngine, restored, agent.StatusPaused)
	gate.abort()
	result := awaitConformanceValue(t, started, "Engine.Start did not return")
	if !errors.Is(result.err, errSimulatedHostCrash) || result.process != nil {
		t.Fatalf("Start result Process=%v error=%v", result.process != nil, result.err)
	}
	finishCrashProcess(t, restored)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashBeforePendingCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitEffectPending, phase: crashCommitBefore,
	})
	deployment, dispatcher := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
		crashSucceededCall(t),
	)
	engine := newCrashEngine(t, gate, nil)
	original := startConformanceProcess(t, engine, deployment, crashInputValue)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	if len(dispatcher.Requests()) != 0 {
		t.Fatal("Dispatcher ran before pending state became authoritative")
	}

	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	result := awaitCrashProcess(t, restored)
	if result.Status() != agent.StatusCompleted || len(dispatcher.Requests()) != 1 {
		t.Fatalf("recomputed result=%s dispatches=%d", result.Status(), len(dispatcher.Requests()))
	}
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashAfterPendingCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitEffectPending, phase: crashCommitAfter,
	})
	deployment, dispatcher := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
	)
	engine := newCrashEngine(t, gate, nil)
	original := startConformanceProcess(t, engine, deployment, crashInputValue)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	if len(dispatcher.Requests()) != 0 {
		t.Fatal("Dispatcher ran before the pending callback returned")
	}

	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	resolveConformanceUnknown(t, restoredEngine, restored, crashInputValue)
	if result := awaitCrashProcess(t, restored); result.Status() != agent.StatusCompleted {
		t.Fatalf("resolved result=%s", result.Status())
	}
	if len(dispatcher.Requests()) != 0 {
		t.Fatalf("never-replay pending Effect dispatches=%d", len(dispatcher.Requests()))
	}
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashBeforeSettledCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitEffectSettled, phase: crashCommitBefore,
	})
	deployment, dispatcher := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
		crashSucceededCall(t),
	)
	engine := newCrashEngine(t, gate, nil)
	original := startConformanceProcess(t, engine, deployment, crashInputValue)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	if len(dispatcher.Requests()) != 1 {
		t.Fatalf("dispatches=%d, want 1", len(dispatcher.Requests()))
	}

	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	resolveConformanceUnknown(t, restoredEngine, restored, crashInputValue)
	if result := awaitCrashProcess(t, restored); result.Status() != agent.StatusCompleted {
		t.Fatalf("resolved result=%s", result.Status())
	}
	if len(dispatcher.Requests()) != 1 {
		t.Fatalf("never-replay redispatched; calls=%d", len(dispatcher.Requests()))
	}
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashAfterSettledCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitEffectSettled, phase: crashCommitAfter,
	})
	deployment, dispatcher := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
		crashSucceededCall(t),
	)
	engine := newCrashEngine(t, gate, nil)
	original := startConformanceProcess(t, engine, deployment, crashInputValue)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	if inspectConformanceProcess(t, engine, original).Status().Terminal() {
		t.Fatal("settled state was applied in memory before its callback returned")
	}

	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	if result := awaitCrashProcess(t, restored); result.Status() != agent.StatusCompleted {
		t.Fatalf("restored settled result=%s", result.Status())
	}
	if len(dispatcher.Requests()) != 1 {
		t.Fatalf("settled Effect redispatched; calls=%d", len(dispatcher.Requests()))
	}
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashAfterParkedCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitCheckpointParked, phase: crashCommitAfter,
	})
	deployment, _ := newCrashDeployment(t, conformanceModePause, agent.ReplayPolicyNever)
	recorder := &ObservationRecorder{}
	engine := newCrashEngine(t, gate, recorder)
	original := startConformanceProcess(t, engine, deployment, crashInputValue)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	assertCrashEventAbsent(t, recorder, agent.EventProcessPaused)

	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	if status := inspectConformanceProcess(t, restoredEngine, restored).Status(); status != agent.StatusPaused {
		t.Fatalf("restored status=%s, want paused", status)
	}
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	finishCrashProcess(t, restored)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashAfterTerminalCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitCheckpointTerminal, phase: crashCommitAfter,
	})
	deployment, _ := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
		crashSucceededCall(t),
	)
	recorder := &ObservationRecorder{}
	engine := newCrashEngine(t, gate, recorder)
	original := startConformanceProcess(t, engine, deployment, crashInputValue)
	awaited := awaitCrashProcessAsync(t.Context(), original)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	select {
	case result := <-awaited:
		t.Fatalf("Result published before terminal callback returned: %+v", result)
	default:
	}
	assertCrashEventAbsent(t, recorder, agent.EventProcessFinished)

	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	if result := awaitCrashProcess(t, restored); result.Status() != agent.StatusCompleted {
		t.Fatalf("restored terminal result=%s", result.Status())
	}
	gate.abort()
	assertCrashRuntimeError(t, original, awaitConformanceValue(t, awaited, "Process did not settle"), errSimulatedHostCrash)
	assertCrashEventAbsent(t, recorder, agent.EventProcessFinished)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashAfterActivationCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	deployment, _ := newCrashDeployment(t, conformanceModePause, agent.ReplayPolicyNever)
	sourceEngine := newCrashEngine(t, store, nil)
	source := startConformanceProcess(t, sourceEngine, deployment, crashInputValue)
	head := waitForConformanceHeadStatus(t, store, source.ID(), agent.StatusPaused)

	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitActivation, phase: crashCommitAfter,
	})
	firstEngine := newCrashEngine(t, gate, nil)
	firstRestore := restoreCrashTreeAsync(t.Context(), firstEngine, deployment, head)
	observation := gate.await(t)
	newHead := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	if _, found := firstEngine.Process(source.ID()); found {
		t.Fatal("restored Process published before activation callback returned")
	}

	secondEngine := newCrashEngine(t, store, nil)
	second := restoreCrashTree(t, secondEngine, deployment, newHead)
	if status := inspectConformanceProcess(t, secondEngine, second).Status(); status != agent.StatusPaused {
		t.Fatalf("second restore status=%s, want paused", status)
	}
	gate.abort()
	first := awaitConformanceValue(t, firstRestore, "Engine.RestoreTree did not return")
	if !errors.Is(first.err, errSimulatedHostCrash) || first.process != nil {
		t.Fatalf("first restore Process=%v error=%v", first.process != nil, first.err)
	}
	closeCrashEngine(t, firstEngine)
	finishCrashProcess(t, second)
	closeCrashEngine(t, secondEngine)
	if err := source.Kill(t.Context(), crashCleanupReason); err != nil {
		t.Fatal(err)
	}
	awaitCrashRuntimeError(t, source, agent.ErrTreeIncarnationConflict)
	closeCrashEngine(t, sourceEngine)
}

func runCrashBeforeProgressCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	runCrashProgressCommit(t, store, crashCommitBefore)
}

func runCrashAfterProgressCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	runCrashProgressCommit(t, store, crashCommitAfter)
}

func runCrashProgressCommit(t *testing.T, store TreeCommitterConformanceDriver, phase crashCommitPhase) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{kind: crashCommitCheckpointProgress, phase: phase})
	deployment, _ := newCrashDeployment(t, conformanceModeProgress, agent.ReplayPolicyNever)
	engine := newCrashEngine(t, gate, nil)
	original := startConformanceProcess(t, engine, deployment, crashInputValue)
	observation := gate.await(t)
	wantSteps := uint64(0)
	if phase == crashCommitAfter {
		wantSteps = 1
	}
	head := assertCrashHead(t, store, observation.rootID(), observation.durableDigest())
	if root := conformanceSnapshotByID(head.ProcessSnapshots(), observation.rootID()); root.Usage().CommittedSteps != wantSteps {
		t.Fatalf("progress committed Steps=%d, want=%d", root.Usage().CommittedSteps, wantSteps)
	}
	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	result := awaitCrashProcess(t, restored)
	if result.Status() != agent.StatusCompleted || result.Usage().CommittedSteps != 2 {
		t.Fatalf("progress recovery result=%s usage=%+v", result.Status(), result.Usage())
	}
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func newCrashDeployment(
	t *testing.T,
	mode conformanceMode,
	replayPolicy agent.ReplayPolicy,
	steps ...ScriptedCall,
) (agent.Deployment, *ScriptedDispatcher) {
	t.Helper()
	descriptor := conformanceDescriptor(t, crashDeploymentName, crashDeploymentDescription)
	dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{
		ReplayPolicy: replayPolicy,
		Calls:        steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           &conformanceDefinition{descriptor: descriptor, mode: mode},
		Dispatcher:           dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte(crashImplementationSeed)),
		ConfigurationDigest:  agent.ComputeDigest([]byte(crashConfigurationSeed)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment, dispatcher
}

func crashSucceededCall(t *testing.T) ScriptedCall {
	t.Helper()
	payload, err := jsonv2.Marshal(conformanceOutput{Value: crashInputValue})
	if err != nil {
		t.Fatal(err)
	}
	return ScriptedCall{
		SettlementStatus:  agent.SettlementStatusSucceeded,
		SettlementPayload: payload,
	}
}

func newCrashEngine(
	t *testing.T,
	committer agent.TreeCommitter,
	recorder *ObservationRecorder,
) *agent.Engine {
	t.Helper()
	config := agent.EngineConfig{TreeCommitter: committer, Budget: agent.Budget{
		Steps: agent.NewQuota(10000), Effects: agent.NewQuota(10000), Signals: agent.NewQuota(100000),
	}}
	if recorder != nil {
		config.EventListeners = []agent.EventListener{recorder}
	}
	engine, err := agent.NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func startCrashProcessAsync(
	t *testing.T,
	engine *agent.Engine,
	deployment agent.Deployment,
) <-chan crashProcessResult {
	t.Helper()
	input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: crashInputValue})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan crashProcessResult, 1)
	go func() {
		process, startErr := engine.Start(t.Context(), deployment, input)
		result <- crashProcessResult{process: process, err: startErr}
	}()
	return result
}

func restoreCrashTree(
	t *testing.T,
	engine *agent.Engine,
	deployment agent.Deployment,
	snapshot agent.TreeSnapshot,
) *agent.Process {
	t.Helper()
	process, err := engine.RestoreTree(t.Context(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func restoreCrashTreeAsync(
	ctx context.Context,
	engine *agent.Engine,
	deployment agent.Deployment,
	snapshot agent.TreeSnapshot,
) <-chan crashProcessResult {
	result := make(chan crashProcessResult, 1)
	go func() {
		process, err := engine.RestoreTree(ctx, deployment, snapshot)
		result <- crashProcessResult{process: process, err: err}
	}()
	return result
}

type treeSnapshotReader interface {
	LoadTree(context.Context, agent.ProcessID) (agent.TreeSnapshot, bool, error)
}

func assertCrashHeadAbsent(
	t *testing.T,
	store treeSnapshotReader,
	rootID agent.ProcessID,
) {
	t.Helper()
	_, exists, err := store.LoadTree(t.Context(), rootID)
	if err != nil || exists {
		t.Fatalf("authoritative head exists=%t error=%v, want absent", exists, err)
	}
}

func assertCrashHead(
	t *testing.T,
	store treeSnapshotReader,
	rootID agent.ProcessID,
	want agent.Digest,
) agent.TreeSnapshot {
	t.Helper()
	head, exists, err := store.LoadTree(t.Context(), rootID)
	if err != nil || !exists || !head.Valid() || head.Digest() != want {
		t.Fatalf(
			"authoritative head exists=%t valid=%t digest=%s want=%s error=%v",
			exists, head.Valid(), head.Digest(), want, err,
		)
	}
	return head
}

func assertCrashEventAbsent(
	t *testing.T,
	recorder *ObservationRecorder,
	name string,
) {
	t.Helper()
	for _, event := range recorder.Events() {
		if event.Name() == name {
			t.Fatalf("Event %s published before durable callback returned", name)
		}
	}
}

func finishCrashProcess(t *testing.T, process *agent.Process) {
	t.Helper()
	if process == nil {
		return
	}
	if err := process.Kill(t.Context(), crashCleanupReason); err != nil && !errors.Is(err, agent.ErrProcessFinished) {
		t.Fatalf("kill Process %s: %v", process.ID(), err)
	}
	awaitCrashProcess(t, process)
}

func closeCrashEngine(t *testing.T, engine *agent.Engine) {
	t.Helper()
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func awaitCrashProcessAsync(ctx context.Context, process *agent.Process) <-chan crashAwaitResult {
	result := make(chan crashAwaitResult, 1)
	go func() {
		value, err := process.Await(ctx)
		result <- crashAwaitResult{result: value, err: err}
	}()
	return result
}

func awaitCrashProcess(t *testing.T, process *agent.Process) agent.Result {
	t.Helper()
	value := awaitConformanceValue(t, awaitCrashProcessAsync(t.Context(), process), "Process did not settle")
	if value.err != nil {
		t.Fatal(value.err)
	}
	return value.result
}

func awaitCrashRuntimeError(t *testing.T, process *agent.Process, cause error) {
	t.Helper()
	value := awaitConformanceValue(t, awaitCrashProcessAsync(t.Context(), process), "Process did not settle")
	assertCrashRuntimeError(t, process, value, cause)
}

func assertCrashRuntimeError(t *testing.T, process *agent.Process, value crashAwaitResult, cause error) {
	t.Helper()
	runtimeErr, ok := errors.AsType[*agent.RuntimeError](value.err)
	if !ok || !errors.Is(value.err, cause) || value.result.Valid() || value.result.Status() != agent.StatusInvalid || value.result.ProcessID().Valid() {
		t.Fatalf("runtime stopped result=%+v error=%v, want cause %v", value.result, value.err, cause)
	}
	if runtimeErr.ProcessID() != process.ID() || !runtimeErr.IncarnationID().Valid() || !runtimeErr.HeadDigest().Valid() {
		t.Fatalf("runtime stopped identity=%+v", runtimeErr)
	}
}

func awaitConformanceValue[T any](t *testing.T, values <-chan T, failure string) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	select {
	case value := <-values:
		return value
	case <-ctx.Done():
		t.Fatalf("%s: %v", failure, ctx.Err())
		var zero T
		return zero
	}
}
