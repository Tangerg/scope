package agenttest

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
)

// TreeCommitterConformanceDriver separates commits from reads so the suite can
// detect acknowledgments that did not install the claimed authoritative head.
type TreeCommitterConformanceDriver interface {
	// TreeCommitter must share storage with LoadTree so the suite can verify
	// acknowledged writes through an independent read.
	agent.TreeCommitter
	// LoadTree must not activate the tree, because observation cannot take
	// ownership from the writer being tested.
	LoadTree(ctx context.Context, rootID agent.ProcessID) (agent.TreeSnapshot, bool, error)
}

// RunTreeCommitterConformance injects failures on both sides of storage commits
// because a lost response must not cause duplicate dispatch or false publication.
// Scenarios cover Unknown resolution, child publication, input consumption,
// budget preservation, subtree cancellation recovery, and tree-local child signal
// and cancel controls, whose parent receipt and recipient state are checked
// through LoadTree.
//
// Each factory call must return an empty isolated store so prior head ownership
// cannot mask a missing compare-and-swap or idempotency check:
//
//	agenttest.RunTreeCommitterConformance(t, func() agenttest.TreeCommitterConformanceDriver {
//		return agent.NewMemoryTreeCommitter()
//	})
//
// Runtime operations inherit the test context; storage calls detach its
// cancellation, and cleanup may outlive it to join owned work. Shutdown
// scenarios release an injected storage gate independently of caller
// cancellation and verify the authoritative head on both sides of the cut.
// Hosts must also fault-inject their real transport to prove its own storage
// deadline and that shutdown interrupts blocked I/O.
func RunTreeCommitterConformance(
	t *testing.T,
	factory func() TreeCommitterConformanceDriver,
) {
	t.Helper()
	if factory == nil {
		t.Fatal("TreeCommitter conformance factory is nil")
	}
	t.Run("effect boundaries and terminal head", func(t *testing.T) {
		runEffectBoundaryConformance(t, factory)
	})
	t.Run("repeated waiting pause resume", func(t *testing.T) {
		runCheckpointCycleConformance(t, factory)
	})
	t.Run("concurrent restore fencing", func(t *testing.T) {
		runConcurrentRestoreConformance(t, factory)
	})
	t.Run("delayed commit loses to activation", func(t *testing.T) {
		runDelayedCommitConformance(t, factory)
	})
	t.Run("crash boundaries", func(t *testing.T) {
		runTreeCommitterCrashConformance(t, factory)
	})
	t.Run("detached storage shutdown", func(t *testing.T) {
		runDetachedStorageShutdownConformance(t, factory)
	})
	t.Run("durable signal admission", func(t *testing.T) {
		runSignalAdmissionConformance(t, factory)
	})
	t.Run("framework child controls", func(t *testing.T) {
		runChildControlConformance(t, factory)
	})
	t.Run("framework child control crashes", func(t *testing.T) {
		runChildControlCrashConformance(t, factory)
	})
}

func runEffectBoundaryConformance(
	t *testing.T,
	factory func() TreeCommitterConformanceDriver,
) {
	t.Helper()
	driver := factory()
	probe := newConformanceDurabilityProbe(t, driver)
	deployment := conformanceDeployment(t, conformanceModeUnknownEffect)
	engine := newConformanceEngine(t, probe)
	head := completeUnknownEffectProcess(t, engine, driver, deployment)
	probe.assertEffectLifecycle(t)
	boundaries := probe.effectBoundaries()
	for _, boundary := range boundaries {
		if err := driver.CommitEffect(t.Context(), boundary); !errors.Is(err, agent.ErrCommitConflict) {
			t.Fatalf("stale %s duplicate error=%v, want ErrCommitConflict", boundary.Kind(), err)
		}
	}
	assertCrashHead(t, driver, head.RootID(), head.Digest())
	closeCrashEngine(t, engine)

	competing := commitCompetingEffectHistory(t, deployment, probe.startCheckpoint())
	if len(competing) != len(boundaries) {
		t.Fatal("competing history missed an Effect boundary")
	}
	for index, boundary := range competing {
		original := boundaries[index]
		if boundary.Kind() != original.Kind() || boundary.Request().ID() != original.Request().ID() ||
			boundary.TreeSnapshot().Digest() == original.TreeSnapshot().Digest() {
			t.Fatal("fixture did not produce conflicting content under the same Effect key")
		}
		if err := driver.CommitEffect(t.Context(), boundary); !errors.Is(err, agent.ErrCommitConflict) &&
			!errors.Is(err, agent.ErrTreeIncarnationConflict) {
			t.Fatalf("conflicting duplicate error=%v", err)
		}
	}
	assertCrashHead(t, driver, head.RootID(), head.Digest())
}

func completeUnknownEffectProcess(
	t *testing.T,
	engine *agent.Engine,
	driver TreeCommitterConformanceDriver,
	deployment agent.Deployment,
) agent.TreeSnapshot {
	t.Helper()
	const value = "committed"
	process := startConformanceProcess(t, engine, deployment, value)
	resolveConformanceUnknown(t, engine, process, value)
	result, err := process.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	head, exists, err := driver.LoadTree(t.Context(), result.ProcessID())
	if err != nil || !exists || !head.Valid() {
		t.Fatalf("authoritative terminal head exists=%t error=%v", exists, err)
	}
	root := conformanceSnapshotByID(head.ProcessSnapshots(), result.ProcessID())
	if !root.Valid() || root.Status() != agent.StatusCompleted {
		t.Fatalf("authoritative root status=%s", root.Status())
	}
	return head
}

// A separate writer produces a valid competing history from the same base, so
// the suite never synthesizes a private snapshot or boundary representation.
func commitCompetingEffectHistory(
	t *testing.T,
	deployment agent.Deployment,
	start agent.TreeCheckpoint,
) []agent.EffectBoundary {
	t.Helper()
	branch := agent.NewMemoryTreeCommitter()
	if err := branch.CommitCheckpoint(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	probe := newConformanceDurabilityProbe(t, branch)
	engine := newConformanceEngine(t, probe)
	process := restoreCrashTree(t, engine, deployment, start.TreeSnapshot())
	resolveConformanceUnknown(t, engine, process, "divergent")
	if err := process.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	closeCrashEngine(t, engine)
	return probe.effectBoundaries()
}

type conformanceRestoreResult struct {
	engine  *agent.Engine
	process *agent.Process
	err     error
}

func runConcurrentRestoreConformance(
	t *testing.T,
	factory func() TreeCommitterConformanceDriver,
) {
	t.Helper()
	driver := factory()
	probe := newConformanceDurabilityProbe(t, driver)
	deployment := conformanceDeployment(t, conformanceModePause)
	originalEngine := newConformanceEngine(t, probe)
	original := startConformanceProcess(t, originalEngine, deployment, "paused")
	waitForConformanceStatus(t, originalEngine, original, agent.StatusPaused)
	head := waitForConformanceHeadStatus(t, driver, original.ID(), agent.StatusPaused)
	probe.assertCheckpoints(t, agent.TreeCheckpointKindStart, agent.TreeCheckpointKindParked)

	results := make(chan conformanceRestoreResult, 2)
	for range 2 {
		go raceConformanceRestore(t.Context(), probe, deployment, head, results)
	}
	winner, conflicts := collectConformanceRestoreResults(t, results)
	if winner.process == nil || conflicts != 1 {
		t.Fatalf("restore winner=%v conflicts=%d", winner.process != nil, conflicts)
	}
	if err := winner.process.Kill(t.Context(), conformanceCleanupReason); err != nil {
		t.Fatal(err)
	}
	if _, err := winner.process.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	closeCrashEngine(t, winner.engine)
	closeConformanceProcess(t, originalEngine, original)
}

func raceConformanceRestore(
	ctx context.Context,
	committer agent.TreeCommitter,
	deployment agent.Deployment,
	head agent.TreeSnapshot,
	results chan<- conformanceRestoreResult,
) {
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: committer})
	if err != nil {
		results <- conformanceRestoreResult{err: err}
		return
	}
	process, err := engine.RestoreTree(ctx, deployment, head)
	results <- conformanceRestoreResult{engine: engine, process: process, err: err}
}

func collectConformanceRestoreResults(
	t *testing.T,
	results <-chan conformanceRestoreResult,
) (conformanceRestoreResult, int) {
	t.Helper()
	var winner conformanceRestoreResult
	conflicts := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			winner = result
			continue
		}
		if !errors.Is(result.err, agent.ErrTreeIncarnationConflict) {
			t.Fatalf("losing restore error=%v", result.err)
		}
		conflicts++
		closeCrashEngine(t, result.engine)
	}
	return winner, conflicts
}

func runDelayedCommitConformance(
	t *testing.T,
	factory func() TreeCommitterConformanceDriver,
) {
	t.Helper()
	driver := factory()
	blocking := newTreeCommitterCommitGate(t, driver, crashCommitPoint{
		kind: crashCommitEffectPending, phase: crashCommitBefore,
	})
	deployment := conformanceDeployment(t, conformanceModeEffect)
	originalEngine := newConformanceEngine(t, blocking)
	original := startConformanceProcess(t, originalEngine, deployment, "fenced")
	blocking.await(t)
	base, exists, err := driver.LoadTree(t.Context(), original.ID())
	if err != nil || !exists || !base.Valid() {
		t.Fatalf("authoritative base head exists=%t error=%v", exists, err)
	}

	restoredEngine := newConformanceEngine(t, blocking)
	restored := restoreCrashTree(t, restoredEngine, deployment, base)
	if result, awaitErr := restored.Await(t.Context()); awaitErr != nil ||
		result.Status() != agent.StatusCompleted {
		t.Fatalf("restored result status=%s error=%v", result.Status(), awaitErr)
	}
	winningHead, exists, err := driver.LoadTree(t.Context(), original.ID())
	if err != nil || !exists || !winningHead.Valid() ||
		conformanceSnapshotByID(winningHead.ProcessSnapshots(), original.ID()).Status() != agent.StatusCompleted {
		t.Fatalf("winner did not publish a durable terminal head: exists=%t error=%v", exists, err)
	}
	blocking.continueCommit()
	stale, err := original.Await(t.Context())
	assertCrashRuntimeError(t, original, crashAwaitResult{result: stale, err: err}, agent.ErrTreeIncarnationConflict)
	assertCrashHead(t, driver, original.ID(), winningHead.Digest())
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, originalEngine)
}

// conformanceDurabilityProbe repeats every successful commit because a
// committer must acknowledge an identical retry; a failed call is never retried.
type conformanceDurabilityProbe struct {
	committer agent.TreeCommitter

	mu          sync.Mutex
	effects     []agent.EffectBoundary
	checkpoints []agent.TreeCheckpoint
	start       agent.TreeCheckpoint
}

func newConformanceDurabilityProbe(
	t *testing.T,
	committer agent.TreeCommitter,
) *conformanceDurabilityProbe {
	t.Helper()
	if lo.IsNil(committer) {
		t.Fatal("TreeCommitter conformance driver returned nil")
	}
	return &conformanceDurabilityProbe{committer: committer}
}

func (c *conformanceDurabilityProbe) ActivateTree(
	ctx context.Context,
	activation agent.TreeActivation,
) error {
	return c.acknowledgeRepeat(func() error { return c.committer.ActivateTree(ctx, activation) })
}

func (c *conformanceDurabilityProbe) CommitEffect(
	ctx context.Context,
	boundary agent.EffectBoundary,
) error {
	err := c.acknowledgeRepeat(func() error { return c.committer.CommitEffect(ctx, boundary) })
	if err == nil {
		c.mu.Lock()
		c.effects = append(c.effects, boundary)
		c.mu.Unlock()
	}
	return err
}

func (c *conformanceDurabilityProbe) CommitCheckpoint(
	ctx context.Context,
	checkpoint agent.TreeCheckpoint,
) error {
	err := c.acknowledgeRepeat(func() error {
		return c.committer.CommitCheckpoint(ctx, checkpoint)
	})
	if err == nil {
		c.mu.Lock()
		c.checkpoints = append(c.checkpoints, checkpoint)
		if checkpoint.Kind() == agent.TreeCheckpointKindStart {
			c.start = checkpoint
		}
		c.mu.Unlock()
	}
	return err
}

func (c *conformanceDurabilityProbe) acknowledgeRepeat(commit func() error) error {
	if err := commit(); err != nil {
		return err
	}
	return commit()
}

func (c *conformanceDurabilityProbe) latestCheckpoint() agent.TreeCheckpoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checkpoints[len(c.checkpoints)-1]
}

func (c *conformanceDurabilityProbe) startCheckpoint() agent.TreeCheckpoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.start
}

func (c *conformanceDurabilityProbe) effectBoundaries() []agent.EffectBoundary {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.effects)
}

func (c *conformanceDurabilityProbe) assertEffectLifecycle(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.effects) != 3 || c.effects[0].Kind() != agent.EffectBoundaryKindPending ||
		c.effects[1].Kind() != agent.EffectBoundaryKindSettled ||
		c.effects[2].Kind() != agent.EffectBoundaryKindResolved {
		t.Fatalf("Effect boundary order=%v", c.effects)
	}
	if len(c.checkpoints) == 0 ||
		c.checkpoints[len(c.checkpoints)-1].Kind() != agent.TreeCheckpointKindTerminal {
		t.Fatalf("checkpoint order=%v", c.checkpoints)
	}
}

func (c *conformanceDurabilityProbe) assertCheckpoints(
	t *testing.T,
	want ...agent.TreeCheckpointKind,
) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	kinds := make([]agent.TreeCheckpointKind, len(c.checkpoints))
	for index, checkpoint := range c.checkpoints {
		kinds[index] = checkpoint.Kind()
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("checkpoint order=%v, want %v", kinds, want)
	}
}

type conformanceMode uint8

const (
	conformanceModeInvalid conformanceMode = iota
	conformanceModeEffect
	conformanceModeUnknownEffect
	conformanceModePause
	conformanceModeProgress
	conformanceModeWait
)

type conformancePhase uint8

const (
	conformancePhaseInvalid conformancePhase = iota
	conformancePhaseReady
	conformancePhaseAwaitingEffect
	conformancePhaseFinished
)

func (c conformancePhase) valid() bool {
	return c == conformancePhaseReady || c == conformancePhaseAwaitingEffect ||
		c == conformancePhaseFinished
}

const (
	conformanceStatusTimeout = 5 * time.Second
	conformancePollInterval  = time.Millisecond
	conformanceCleanupReason = "conformance cleanup"
)

type conformanceInput struct {
	Value string `json:"value"`
}

type conformanceOutput struct {
	Value string `json:"value"`
}

type conformanceState struct {
	Phase conformancePhase `json:"phase"`
	Value string           `json:"value"`
}

type conformanceDefinition struct {
	descriptor agent.Descriptor
	mode       conformanceMode
}

func conformanceDeployment(t *testing.T, mode conformanceMode) agent.Deployment {
	t.Helper()
	descriptor := conformanceDescriptor(t, "agenttest.committer_conformance",
		"Exercises the complete durable tree commit contract.")
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           &conformanceDefinition{descriptor: descriptor, mode: mode},
		Dispatcher:           conformanceDispatcher{unknown: mode == conformanceModeUnknownEffect},
		ImplementationDigest: agent.ComputeDigest([]byte("agenttest committer implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte{byte(mode)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func conformanceDescriptor(t *testing.T, name, description string) agent.Descriptor {
	t.Helper()
	inputSchema, err := agent.SchemaFor[conformanceInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := agent.SchemaFor[conformanceOutput]()
	if err != nil {
		t.Fatal(err)
	}
	signalSchema, err := agent.ParseSchema([]byte("true"))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: name, Description: description,
		InputSchema: inputSchema, OutputSchema: outputSchema, SignalSchema: signalSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func (c *conformanceDefinition) Descriptor() agent.Descriptor { return c.descriptor }

func (*conformanceDefinition) ChildDeployments() []agent.Deployment { return nil }

func (c *conformanceDefinition) Start(input agent.Payload) (agent.Execution, error) {
	value, err := input.Decode[conformanceInput]()
	if err != nil {
		return nil, err
	}
	return &conformanceExecution{
		definition: c,
		state:      conformanceState{Phase: conformancePhaseReady, Value: value.Value},
	}, nil
}

func (c *conformanceDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	value, err := state.Decode[conformanceState](c.descriptor.Name())
	if err != nil {
		return nil, err
	}
	if !value.Phase.valid() {
		return nil, agent.ErrInvalidExecutionState
	}
	return &conformanceExecution{definition: c, state: value}, nil
}

type conformanceExecution struct {
	definition *conformanceDefinition
	state      conformanceState
}

func (c *conformanceExecution) Step(
	_ context.Context,
	signals []agent.Signal,
) (agent.Transition, error) {
	switch c.definition.mode {
	case conformanceModeWait:
		return c.stepWait(signals)
	case conformanceModeProgress:
		return c.stepProgress()
	case conformanceModeEffect, conformanceModeUnknownEffect:
		return c.stepEffect(signals)
	case conformanceModePause:
		if c.state.Phase != conformancePhaseReady || len(signals) != 0 {
			return agent.Transition{}, errors.New("agenttest: paused execution cannot advance")
		}
		c.state.Phase = conformancePhaseFinished
		return agent.Pause(0, "committer conformance parked state")
	default:
		return agent.Transition{}, errors.New("agenttest: invalid conformance mode")
	}
}

func (c *conformanceExecution) stepWait(signals []agent.Signal) (agent.Transition, error) {
	if c.state.Phase == conformancePhaseReady {
		key, err := agent.ParseWaitKey("committer_question")
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewWaitEffect(key)
		if err != nil {
			return agent.Transition{}, err
		}
		c.state.Phase = conformancePhaseAwaitingEffect
		return agent.Continue(0, effect)
	}
	if c.state.Phase != conformancePhaseAwaitingEffect || len(signals) != 1 {
		return agent.Transition{}, errors.New("agenttest: missing wait opening signal")
	}
	waitID, ok := signals[0].WaitID()
	if !ok {
		return agent.Transition{}, errors.New("agenttest: missing wait identity")
	}
	c.state.Phase = conformancePhaseFinished
	return agent.Wait(1, waitID)
}

func (c *conformanceExecution) stepProgress() (agent.Transition, error) {
	if c.state.Phase == conformancePhaseReady {
		c.state.Phase = conformancePhaseAwaitingEffect
		return agent.Checkpoint(0)
	}
	c.state.Phase = conformancePhaseFinished
	output, err := agent.EncodePayload(conformanceOutput{Value: c.state.Value})
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(0, output)
}

func (c *conformanceExecution) stepEffect(signals []agent.Signal) (agent.Transition, error) {
	switch c.state.Phase {
	case conformancePhaseReady:
		if len(signals) != 0 {
			return agent.Transition{}, errors.New("agenttest: unexpected initial Signal")
		}
		c.state.Phase = conformancePhaseAwaitingEffect
		payload, err := jsonv2.Marshal(conformanceInput{Value: c.state.Value})
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewDispatcherEffect(payload)
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Continue(0, effect)
	case conformancePhaseAwaitingEffect:
		if len(signals) != 1 {
			return agent.Transition{}, errors.New("agenttest: settlement Signal is missing")
		}
		var output conformanceOutput
		if err := jsonv2.Unmarshal(signals[0].Payload(), &output); err != nil {
			return agent.Transition{}, err
		}
		c.state.Phase = conformancePhaseFinished
		encoded, err := agent.EncodePayload(output)
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Complete(1, encoded)
	default:
		return agent.Transition{}, errors.New("agenttest: completed execution cannot advance")
	}
}

func (c *conformanceExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := jsonv2.Marshal(c.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.ParseExecutionState(c.definition.descriptor.Name(), payload)
}

type conformanceDispatcher struct {
	unknown bool
}

func (c conformanceDispatcher) Dispatch(
	_ context.Context,
	request agent.EffectRequest,
	_ agent.DeltaEmitter,
) (agent.Settlement, error) {
	var input conformanceInput
	if err := jsonv2.Unmarshal(request.Effect().Payload(), &input); err != nil {
		return agent.Settlement{}, err
	}
	payload, err := jsonv2.Marshal(conformanceOutput(input))
	if err != nil {
		return agent.Settlement{}, err
	}
	status := agent.SettlementStatusSucceeded
	if c.unknown {
		status = agent.SettlementStatusUnknown
	}
	return agent.NewSettlement(status, payload)
}

func (conformanceDispatcher) Policy(agent.Effect) agent.EffectPolicy {
	return agent.EffectPolicy{Replay: agent.ReplayPolicyNever}
}

func newConformanceEngine(t *testing.T, committer agent.TreeCommitter) *agent.Engine {
	t.Helper()
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func startConformanceProcess(
	t *testing.T,
	engine *agent.Engine,
	deployment agent.Deployment,
	value string,
) *agent.Process {
	t.Helper()
	input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: value})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func resolveConformanceUnknown(t *testing.T, engine *agent.Engine, process *agent.Process, value string) {
	t.Helper()
	effectID := waitForConformanceUnknownEffect(t, engine, process)
	if err := process.ResolveUnknownEffect(t.Context(), effectID, conformanceResolution(t, value)); err != nil {
		t.Fatal(err)
	}
}

func conformanceResolution(t *testing.T, value string) agent.Settlement {
	t.Helper()
	payload, err := jsonv2.Marshal(conformanceOutput{Value: value})
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := agent.NewSettlement(agent.SettlementStatusSucceeded, payload)
	if err != nil {
		t.Fatal(err)
	}
	return settlement
}

// closeConformanceProcess tolerates only the errors a deliberately crashed or
// fenced writer reports, because cleanup follows scenarios that cut them.
func closeConformanceProcess(t *testing.T, engine *agent.Engine, process *agent.Process) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), conformanceStatusTimeout)
	defer cancel()
	stopped := func(err error) bool {
		return err == nil || errors.Is(err, errSimulatedHostCrash) || errors.Is(err, agent.ErrTreeIncarnationConflict)
	}
	if err := process.Kill(ctx, conformanceCleanupReason); !stopped(err) && !errors.Is(err, agent.ErrProcessFinished) {
		t.Error(err)
	}
	if _, err := process.Await(ctx); !stopped(err) {
		t.Error(err)
	}
	if err := process.Join(ctx); !stopped(err) {
		t.Error(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Error(err)
	}
}

func pollConformanceProcess(
	t *testing.T,
	engine *agent.Engine,
	process *agent.Process,
	done func(agent.ProcessSnapshot) bool,
) (agent.ProcessSnapshot, bool) {
	t.Helper()
	deadline := time.NewTimer(conformanceStatusTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(conformancePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if snapshot := inspectConformanceProcess(t, engine, process); done(snapshot) {
				return snapshot, true
			}
		case <-deadline.C:
			return inspectConformanceProcess(t, engine, process), false
		}
	}
}

func waitForConformanceUnknownEffect(
	t *testing.T,
	engine *agent.Engine,
	process *agent.Process,
) agent.EffectID {
	t.Helper()
	snapshot, found := pollConformanceProcess(t, engine, process, func(snapshot agent.ProcessSnapshot) bool {
		return len(snapshot.UnknownEffectIDs()) == 1
	})
	if !found {
		t.Fatal("Process did not expose one Unknown Effect")
	}
	return snapshot.UnknownEffectIDs()[0]
}

func waitForConformanceStatus(
	t *testing.T,
	engine *agent.Engine,
	process *agent.Process,
	want agent.Status,
) {
	t.Helper()
	snapshot, reached := pollConformanceProcess(t, engine, process, func(snapshot agent.ProcessSnapshot) bool {
		return snapshot.Status() == want
	})
	if !reached {
		t.Fatalf("Process status=%s, want %s", snapshot.Status(), want)
	}
}

func conformanceSnapshotByID(
	snapshots []agent.ProcessSnapshot,
	processID agent.ProcessID,
) agent.ProcessSnapshot {
	for _, snapshot := range snapshots {
		if snapshot.ProcessID() == processID {
			return snapshot
		}
	}
	return agent.ProcessSnapshot{}
}

func waitForConformanceHeadStatus(
	t *testing.T,
	driver TreeCommitterConformanceDriver,
	rootID agent.ProcessID,
	want agent.Status,
) agent.TreeSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	ticker := time.NewTicker(conformancePollInterval)
	defer ticker.Stop()
	for {
		head, exists, err := driver.LoadTree(ctx, rootID)
		if err == nil && exists && head.Valid() {
			root := conformanceSnapshotByID(head.ProcessSnapshots(), rootID)
			if root.Valid() && root.Status() == want {
				return head
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("authoritative root status did not become %s: last error=%v", want, err)
		}
	}
}

func inspectConformanceProcess(t *testing.T, engine *agent.Engine, process *agent.Process) agent.ProcessSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	inspection, err := engine.InspectTree(ctx, process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	report, found := inspection.Process(process.ID())
	if !found {
		t.Fatal("Process is missing from its runtime inspection")
	}
	return report.Snapshot
}

var _ agent.Definition = (*conformanceDefinition)(nil)
