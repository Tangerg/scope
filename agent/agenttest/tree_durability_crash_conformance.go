package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

type crashCommitKind uint8

const (
	crashCommitInvalid crashCommitKind = iota
	crashCommitRootOutcome
	crashCommitActivation
	crashCommitEffectPending
	crashCommitEffectSettled
	crashCommitCheckpointInput
	crashCommitCheckpointParked
	crashCommitCheckpointTerminal
)

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
	rootID         agent.ProcessID
	previousDigest agent.Digest
	prospective    agent.TreeSnapshot
}

var errSimulatedHostCrash = errors.New("agenttest: simulated host crash")

// treeDurabilityCommitGate cuts the callback itself, not an Engine goroutine.
// An after cut occurs after the delegate has advanced its authoritative head
// and before the callback can return to the Runtime for in-memory apply.
type treeDurabilityCommitGate struct {
	delegate agent.TreeDurability
	point    crashCommitPoint

	mu      sync.Mutex
	claimed bool

	reached  chan crashCommitObservation
	decision chan error
	resolve  sync.Once
}

func newTreeDurabilityCommitGate(
	t *testing.T,
	delegate agent.TreeDurability,
	point crashCommitPoint,
) *treeDurabilityCommitGate {
	t.Helper()
	if delegate == nil || !point.valid() {
		t.Fatal("invalid tree durability crash gate")
	}
	gate := &treeDurabilityCommitGate{
		delegate: delegate,
		point:    point,
		reached:  make(chan crashCommitObservation, 1),
		decision: make(chan error, 1),
	}
	t.Cleanup(func() { gate.abort() })
	return gate
}

func (t *treeDurabilityCommitGate) AcknowledgeProcessStartOutcome(
	ctx context.Context,
	outcome agent.ProcessStartOutcome,
) error {
	snapshot, _ := outcome.TreeSnapshot()
	previous, _ := outcome.PreviousTreeDigest()
	observation := crashCommitObservation{
		rootID: outcome.Admission().Relation().RootID(), previousDigest: previous,
		prospective: snapshot,
	}
	kind := crashCommitInvalid
	if outcome.Admission().Relation().IsRoot() {
		kind = crashCommitRootOutcome
	}
	point := crashCommitPoint{kind: kind, phase: t.point.phase}
	return t.around(point, observation, func() error {
		return t.delegate.AcknowledgeProcessStartOutcome(ctx, outcome)
	})
}

func (t *treeDurabilityCommitGate) ActivateTree(
	ctx context.Context,
	activation agent.TreeActivation,
) error {
	observation := crashCommitObservation{
		rootID:         activation.TreeSnapshot().RootID(),
		previousDigest: activation.PreviousTreeDigest(),
		prospective:    activation.TreeSnapshot(),
	}
	point := crashCommitPoint{kind: crashCommitActivation, phase: t.point.phase}
	return t.around(point, observation, func() error {
		return t.delegate.ActivateTree(ctx, activation)
	})
}

func (t *treeDurabilityCommitGate) CommitEffect(
	ctx context.Context,
	boundary agent.EffectBoundary,
) error {
	kind := crashCommitInvalid
	switch boundary.Kind() {
	case agent.EffectBoundaryPending:
		kind = crashCommitEffectPending
	case agent.EffectBoundarySettled:
		kind = crashCommitEffectSettled
	}
	observation := crashCommitObservation{
		rootID:         boundary.TreeSnapshot().RootID(),
		previousDigest: boundary.PreviousTreeDigest(),
		prospective:    boundary.TreeSnapshot(),
	}
	point := crashCommitPoint{kind: kind, phase: t.point.phase}
	return t.around(point, observation, func() error {
		return t.delegate.CommitEffect(ctx, boundary)
	})
}

func (t *treeDurabilityCommitGate) CommitCheckpoint(
	ctx context.Context,
	checkpoint agent.TreeCheckpoint,
) error {
	kind := crashCommitInvalid
	switch checkpoint.Kind() {
	case agent.TreeCheckpointInput:
		kind = crashCommitCheckpointInput
	case agent.TreeCheckpointParked:
		kind = crashCommitCheckpointParked
	case agent.TreeCheckpointTerminal:
		kind = crashCommitCheckpointTerminal
	}
	observation := crashCommitObservation{
		rootID:         checkpoint.TreeSnapshot().RootID(),
		previousDigest: checkpoint.PreviousTreeDigest(),
		prospective:    checkpoint.TreeSnapshot(),
	}
	point := crashCommitPoint{kind: kind, phase: t.point.phase}
	return t.around(point, observation, func() error {
		return t.delegate.CommitCheckpoint(ctx, checkpoint)
	})
}

func (t *treeDurabilityCommitGate) around(
	point crashCommitPoint,
	observation crashCommitObservation,
	commit func() error,
) error {
	if point.kind != t.point.kind {
		return commit()
	}
	if point.phase == crashCommitBefore && t.claim() {
		if err := t.cut(observation); err != nil {
			return err
		}
	}
	if err := commit(); err != nil {
		return err
	}
	if point.phase == crashCommitAfter && t.claim() {
		return t.cut(observation)
	}
	return nil
}

func (t *treeDurabilityCommitGate) claim() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.claimed {
		return false
	}
	t.claimed = true
	return true
}

func (t *treeDurabilityCommitGate) cut(observation crashCommitObservation) error {
	t.reached <- observation
	return <-t.decision
}

func (t *treeDurabilityCommitGate) await(test *testing.T) crashCommitObservation {
	test.Helper()
	ctx, cancel := context.WithTimeout(test.Context(), conformanceStatusTimeout)
	defer cancel()
	select {
	case observation := <-t.reached:
		return observation
	case <-ctx.Done():
		test.Fatalf("durability gate was not reached: %v", ctx.Err())
		return crashCommitObservation{}
	}
}

func (t *treeDurabilityCommitGate) continueCommit() {
	t.resolve.Do(func() { t.decision <- nil })
}

func (t *treeDurabilityCommitGate) abort() {
	t.resolve.Do(func() { t.decision <- errSimulatedHostCrash })
}

type crashStartResult struct {
	process *agent.Process
	err     error
}

type crashRestoreResult struct {
	process *agent.Process
	err     error
}

type crashAwaitResult struct {
	result agent.Result
	err    error
}

const (
	crashDeploymentName        = "agenttest.durability_crash"
	crashDeploymentDescription = "Exercises exact durable crash prefixes."
	crashImplementationSeed    = "agenttest durability crash implementation"
	crashConfigurationSeed     = "agenttest durability crash configuration"
	crashInputValue            = "crash-prefix"
	crashCleanupReason         = "durability crash matrix cleanup"
)

func runTreeDurabilityCrashConformance(t *testing.T, factory func() TreeDurabilityConformanceDriver) {
	t.Helper()
	tests := []struct {
		name string
		run  func(*testing.T, TreeDurabilityConformanceDriver)
	}{
		{name: "root outcome before commit", run: runCrashBeforeRootOutcomeCommit},
		{name: "root outcome after commit before Process publication", run: runCrashAfterRootOutcomeCommit},
		{name: "pending before commit", run: runCrashBeforePendingCommit},
		{name: "pending after commit before dispatch", run: runCrashAfterPendingCommit},
		{name: "after dispatch before settled commit", run: runCrashBeforeSettledCommit},
		{name: "settled after commit before memory apply", run: runCrashAfterSettledCommit},
		{name: "parked after commit before Event publication", run: runCrashAfterParkedCommit},
		{name: "terminal after commit before Result publication", run: runCrashAfterTerminalCommit},
		{name: "activation after CAS before Process publication", run: runCrashAfterActivationCommit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { test.run(t, factory()) })
	}
}

func runCrashBeforeRootOutcomeCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitRootOutcome, phase: crashCommitBefore,
	})
	deployment, _ := newCrashDeployment(t, conformanceModePause, agent.ReplayPolicyNever)
	engine := newCrashEngine(t, gate, nil)
	started := startCrashProcessAsync(t, engine, deployment)
	observation := gate.await(t)
	assertCrashHeadAbsent(t, store, observation.rootID)
	if _, found := engine.Process(observation.rootID); found {
		t.Fatal("root Process published before its base head commit")
	}
	gate.abort()
	result := awaitCrashStart(t, started)
	if !errors.Is(result.err, errSimulatedHostCrash) || result.process != nil {
		t.Fatalf("Start result Process=%v error=%v", result.process != nil, result.err)
	}
	closeCrashEngine(t, engine)
}

func runCrashAfterRootOutcomeCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitRootOutcome, phase: crashCommitAfter,
	})
	deployment, _ := newCrashDeployment(t, conformanceModePause, agent.ReplayPolicyNever)
	engine := newCrashEngine(t, gate, nil)
	started := startCrashProcessAsync(t, engine, deployment)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID, observation.prospective.Digest())
	if _, found := engine.Process(observation.rootID); found {
		t.Fatal("root Process published before its committed base callback returned")
	}

	restoredEngine := newCrashEngine(t, durability, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	waitForConformanceStatus(t, restored, agent.StatusPaused)
	gate.abort()
	result := awaitCrashStart(t, started)
	if !errors.Is(result.err, errSimulatedHostCrash) || result.process != nil {
		t.Fatalf("Start result Process=%v error=%v", result.process != nil, result.err)
	}
	finishCrashProcess(t, restored)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashBeforePendingCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitEffectPending, phase: crashCommitBefore,
	})
	deployment, dispatcher := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
		crashSucceededDispatchStep(t),
	)
	engine := newCrashEngine(t, gate, nil)
	original := startCrashProcess(t, engine, deployment)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID, observation.previousDigest)
	if len(dispatcher.Requests()) != 0 {
		t.Fatal("Dispatcher ran before pending state became authoritative")
	}

	restoredEngine := newCrashEngine(t, durability, nil)
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

func runCrashAfterPendingCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitEffectPending, phase: crashCommitAfter,
	})
	deployment, dispatcher := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
	)
	engine := newCrashEngine(t, gate, nil)
	original := startCrashProcess(t, engine, deployment)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID, observation.prospective.Digest())
	if len(dispatcher.Requests()) != 0 {
		t.Fatal("Dispatcher ran before the pending callback returned")
	}

	restoredEngine := newCrashEngine(t, durability, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	resolveCrashUnknown(t, restored)
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

func runCrashBeforeSettledCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitEffectSettled, phase: crashCommitBefore,
	})
	deployment, dispatcher := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
		crashSucceededDispatchStep(t),
	)
	engine := newCrashEngine(t, gate, nil)
	original := startCrashProcess(t, engine, deployment)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID, observation.previousDigest)
	if len(dispatcher.Requests()) != 1 {
		t.Fatalf("dispatches=%d, want 1", len(dispatcher.Requests()))
	}

	restoredEngine := newCrashEngine(t, durability, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	resolveCrashUnknown(t, restored)
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

func runCrashAfterSettledCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitEffectSettled, phase: crashCommitAfter,
	})
	deployment, dispatcher := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
		crashSucceededDispatchStep(t),
	)
	engine := newCrashEngine(t, gate, nil)
	original := startCrashProcess(t, engine, deployment)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID, observation.prospective.Digest())
	if original.Status().Terminal() {
		t.Fatal("settled state was applied in memory before its callback returned")
	}

	restoredEngine := newCrashEngine(t, durability, nil)
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

func runCrashAfterParkedCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitCheckpointParked, phase: crashCommitAfter,
	})
	deployment, _ := newCrashDeployment(t, conformanceModePause, agent.ReplayPolicyNever)
	recorder := &ObservationRecorder{}
	engine := newCrashEngine(t, gate, recorder)
	original := startCrashProcess(t, engine, deployment)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID, observation.prospective.Digest())
	assertCrashEventAbsent(t, recorder, agent.EventProcessPaused)

	restoredEngine := newCrashEngine(t, durability, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	if restored.Status() != agent.StatusPaused {
		t.Fatalf("restored status=%s, want paused", restored.Status())
	}
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	finishCrashProcess(t, restored)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashAfterTerminalCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitCheckpointTerminal, phase: crashCommitAfter,
	})
	deployment, _ := newCrashDeployment(
		t, conformanceModeEffect, agent.ReplayPolicyNever,
		crashSucceededDispatchStep(t),
	)
	recorder := &ObservationRecorder{}
	engine := newCrashEngine(t, gate, recorder)
	original := startCrashProcess(t, engine, deployment)
	awaited := awaitCrashProcessAsync(original)
	observation := gate.await(t)
	head := assertCrashHead(t, store, observation.rootID, observation.prospective.Digest())
	select {
	case result := <-awaited:
		t.Fatalf("Result published before terminal callback returned: %+v", result)
	default:
	}
	assertCrashEventAbsent(t, recorder, agent.EventProcessFinished)

	restoredEngine := newCrashEngine(t, durability, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	if result := awaitCrashProcess(t, restored); result.Status() != agent.StatusCompleted {
		t.Fatalf("restored terminal result=%s", result.Status())
	}
	gate.abort()
	assertCrashRuntimeError(t, original, awaitCrashAwait(t, awaited), errSimulatedHostCrash)
	assertCrashEventAbsent(t, recorder, agent.EventProcessFinished)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashAfterActivationCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	deployment, _ := newCrashDeployment(t, conformanceModePause, agent.ReplayPolicyNever)
	sourceEngine := newCrashEngine(t, durability, nil)
	source := startCrashProcess(t, sourceEngine, deployment)
	head := waitForConformanceHeadStatus(t, store, source.ID(), agent.StatusPaused)

	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitActivation, phase: crashCommitAfter,
	})
	firstEngine := newCrashEngine(t, gate, nil)
	firstRestore := restoreCrashTreeAsync(firstEngine, deployment, head)
	observation := gate.await(t)
	newHead := assertCrashHead(t, store, observation.rootID, observation.prospective.Digest())
	if _, found := firstEngine.Process(source.ID()); found {
		t.Fatal("restored Process published before activation callback returned")
	}

	secondEngine := newCrashEngine(t, durability, nil)
	second := restoreCrashTree(t, secondEngine, deployment, newHead)
	if second.Status() != agent.StatusPaused {
		t.Fatalf("second restore status=%s, want paused", second.Status())
	}
	gate.abort()
	first := awaitCrashRestore(t, firstRestore)
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

func newCrashDeployment(
	t *testing.T,
	mode conformanceMode,
	replayPolicy agent.ReplayPolicy,
	steps ...DispatchStep,
) (agent.Deployment, *ScriptedDispatcher) {
	t.Helper()
	inputSchema, err := agent.SchemaFor[conformanceInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := agent.SchemaFor[conformanceOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: crashDeploymentName, Description: crashDeploymentDescription,
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{
		ReplayPolicy: replayPolicy,
		Steps:        steps,
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

func crashSucceededDispatchStep(t *testing.T) DispatchStep {
	t.Helper()
	payload, err := json.Marshal(conformanceOutput{Value: crashInputValue})
	if err != nil {
		t.Fatal(err)
	}
	return DispatchStep{
		SettlementStatus:  agent.SettlementStatusSucceeded,
		SettlementPayload: payload,
	}
}

func newCrashEngine(
	t *testing.T,
	durability agent.TreeDurability,
	recorder *ObservationRecorder,
) *agent.Engine {
	t.Helper()
	config := agent.EngineConfig{TreeDurability: durability}
	if recorder != nil {
		config.EventListeners = []agent.EventListener{recorder}
	}
	engine, err := agent.NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func startCrashProcess(
	t *testing.T,
	engine *agent.Engine,
	deployment agent.Deployment,
) *agent.Process {
	t.Helper()
	input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: crashInputValue})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func startCrashProcessAsync(
	t *testing.T,
	engine *agent.Engine,
	deployment agent.Deployment,
) <-chan crashStartResult {
	t.Helper()
	input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: crashInputValue})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan crashStartResult, 1)
	go func() {
		process, startErr := engine.Start(t.Context(), deployment, input)
		result <- crashStartResult{process: process, err: startErr}
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
	engine *agent.Engine,
	deployment agent.Deployment,
	snapshot agent.TreeSnapshot,
) <-chan crashRestoreResult {
	result := make(chan crashRestoreResult, 1)
	go func() {
		process, err := engine.RestoreTree(context.Background(), deployment, snapshot)
		result <- crashRestoreResult{process: process, err: err}
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

func resolveCrashUnknown(t *testing.T, process *agent.Process) {
	t.Helper()
	effectID := waitForConformanceUnknownEffect(t, process)
	payload, err := json.Marshal(conformanceOutput{Value: crashInputValue})
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := agent.NewSettlement(
		effectID, agent.SettlementStatusSucceeded, payload,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.ResolveUnknownEffect(t.Context(), settlement); err != nil {
		t.Fatal(err)
	}
}

func finishCrashProcess(t *testing.T, process *agent.Process) {
	t.Helper()
	if process == nil {
		return
	}
	if !process.Status().Terminal() {
		err := process.Kill(t.Context(), crashCleanupReason)
		if err != nil && !errors.Is(err, agent.ErrProcessFinished) {
			t.Fatalf("kill Process %s: %v", process.ID(), err)
		}
	}
	awaitCrashProcess(t, process)
}

func closeCrashEngine(t *testing.T, engine *agent.Engine) {
	t.Helper()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func awaitCrashProcessAsync(process *agent.Process) <-chan crashAwaitResult {
	result := make(chan crashAwaitResult, 1)
	go func() {
		value, err := process.Await(context.Background())
		result <- crashAwaitResult{result: value, err: err}
	}()
	return result
}

func awaitCrashProcess(t *testing.T, process *agent.Process) agent.Result {
	t.Helper()
	value := awaitCrashAwait(t, awaitCrashProcessAsync(process))
	if value.err != nil {
		t.Fatal(value.err)
	}
	return value.result
}

func awaitCrashRuntimeError(t *testing.T, process *agent.Process, cause error) {
	t.Helper()
	assertCrashRuntimeError(t, process, awaitCrashAwait(t, awaitCrashProcessAsync(process)), cause)
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

func awaitCrashAwait(t *testing.T, result <-chan crashAwaitResult) crashAwaitResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		t.Fatalf("Process did not settle: %v", ctx.Err())
		return crashAwaitResult{}
	}
}

func awaitCrashStart(t *testing.T, result <-chan crashStartResult) crashStartResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		t.Fatalf("Engine.Start did not return: %v", ctx.Err())
		return crashStartResult{}
	}
}

func awaitCrashRestore(t *testing.T, result <-chan crashRestoreResult) crashRestoreResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		t.Fatalf("Engine.RestoreTree did not return: %v", ctx.Err())
		return crashRestoreResult{}
	}
}
