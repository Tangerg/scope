package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
)

// TreeDurabilityConformanceDriver separates commits from reads so the suite can
// detect acknowledgments that did not install the claimed authoritative head.
type TreeDurabilityConformanceDriver interface {
	// TreeDurability must share storage with LoadTree so the suite can verify
	// acknowledged writes through an independent read.
	TreeDurability() agent.TreeDurability
	// LoadTree must not activate the tree, because observation cannot take
	// ownership from the writer being tested.
	LoadTree(ctx context.Context, rootID agent.ProcessID) (agent.TreeSnapshot, bool, error)
}

// RunTreeDurabilityConformance injects failures on both sides of storage commits
// because a lost response must not cause duplicate dispatch or false publication.
// Scenarios include explicit Unknown resolution, child publication, subsequent
// input consumption, budget preservation, and subtree cancellation recovery.
// Each factory call must return an empty isolated store so prior head ownership
// cannot mask a missing compare-and-swap or idempotency check.
func RunTreeDurabilityConformance(
	t *testing.T,
	factory func() TreeDurabilityConformanceDriver,
) {
	t.Helper()
	if factory == nil {
		t.Fatal("TreeDurability conformance factory is nil")
	}
	t.Run("effect boundaries and terminal head", func(t *testing.T) {
		runEffectBoundaryConformance(t, factory)
	})

	t.Run("concurrent restore fencing", func(t *testing.T) {
		runConcurrentRestoreConformance(t, factory)
	})

	t.Run("delayed commit loses to activation", func(t *testing.T) {
		runDelayedCommitConformance(t, factory)
	})
	t.Run("crash boundaries", func(t *testing.T) {
		runTreeDurabilityCrashConformance(t, factory)
	})
	t.Run("durable signal admission", func(t *testing.T) {
		runSignalAdmissionConformance(t, factory)
	})
}

func runEffectBoundaryConformance(
	t *testing.T,
	factory func() TreeDurabilityConformanceDriver,
) {
	t.Helper()
	driver := factory()
	probe := newConformanceDurabilityProbe(t, driver.TreeDurability())
	deployment := conformanceDeployment(t, conformanceModeUnknownEffect)
	engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: probe})
	if err != nil {
		t.Fatal(err)
	}
	input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: "committed"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	effectID := waitForConformanceUnknownEffect(t, engine, process)
	payload, err := json.Marshal(conformanceOutput{Value: "committed"})
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := agent.NewSettlement(
		effectID, agent.SettlementStatusSucceeded, payload,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolveErr := process.ResolveUnknownEffect(context.Background(), settlement); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	head, exists, err := driver.LoadTree(context.Background(), result.ProcessID())
	if err != nil || !exists || !head.Valid() {
		t.Fatalf("authoritative terminal head exists=%t error=%v", exists, err)
	}
	root := conformanceSnapshotByID(head.ProcessSnapshots(), result.ProcessID())
	if !root.Valid() || root.Status() != agent.StatusCompleted {
		t.Fatalf("authoritative root status=%s", root.Status())
	}
	probe.assertEffectLifecycle(t)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type conformanceRestoreResult struct {
	engine  *agent.Engine
	process *agent.Process
	err     error
}

func runConcurrentRestoreConformance(
	t *testing.T,
	factory func() TreeDurabilityConformanceDriver,
) {
	t.Helper()
	driver := factory()
	probe := newConformanceDurabilityProbe(t, driver.TreeDurability())
	deployment := conformanceDeployment(t, conformanceModePause)
	originalEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: probe})
	if err != nil {
		t.Fatal(err)
	}
	input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: "paused"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := originalEngine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForConformanceStatus(t, originalEngine, original, agent.StatusPaused)
	head := waitForConformanceHeadStatus(t, driver, original.ID(), agent.StatusPaused)
	probe.assertCheckpoints(t, agent.TreeCheckpointStart, agent.TreeCheckpointParked)

	results := make(chan conformanceRestoreResult, 2)
	for range 2 {
		go restoreConformanceTree(probe, deployment, head, results)
	}
	winner, conflicts := collectConformanceRestoreResults(t, results)
	if winner.process == nil || conflicts != 1 {
		t.Fatalf("restore winner=%v conflicts=%d", winner.process != nil, conflicts)
	}
	if err := winner.process.Kill(context.Background(), "conformance cleanup"); err != nil {
		t.Fatal(err)
	}
	if _, err := winner.process.Await(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := winner.engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	_ = original.Kill(context.Background(), "stale writer cleanup")
	_, _ = original.Await(context.Background())
	if err := originalEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func restoreConformanceTree(
	durability agent.TreeDurability,
	deployment agent.Deployment,
	head agent.TreeSnapshot,
	results chan<- conformanceRestoreResult,
) {
	engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: durability})
	if err != nil {
		results <- conformanceRestoreResult{err: err}
		return
	}
	process, err := engine.RestoreTree(context.Background(), deployment, head)
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
		if result.engine != nil {
			_ = result.engine.Close(context.WithoutCancel(t.Context()))
		}
	}
	return winner, conflicts
}

func runDelayedCommitConformance(
	t *testing.T,
	factory func() TreeDurabilityConformanceDriver,
) {
	t.Helper()
	driver := factory()
	blocking := newTreeDurabilityCommitGate(t, driver.TreeDurability(), crashCommitPoint{
		kind: crashCommitEffectPending, phase: crashCommitBefore,
	})
	deployment := conformanceDeployment(t, conformanceModeEffect)
	originalEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: blocking})
	if err != nil {
		t.Fatal(err)
	}
	input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: "fenced"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := originalEngine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	blocking.await(t)
	base, exists, err := driver.LoadTree(context.Background(), original.ID())
	if err != nil || !exists || !base.Valid() {
		t.Fatalf("authoritative base head exists=%t error=%v", exists, err)
	}

	restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: blocking})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, base)
	if err != nil {
		t.Fatal(err)
	}
	if result, awaitErr := restored.Await(context.Background()); awaitErr != nil ||
		result.Status() != agent.StatusCompleted {
		t.Fatalf("restored result status=%s error=%v", result.Status(), awaitErr)
	}
	winningHead, exists, err := driver.LoadTree(t.Context(), original.ID())
	if err != nil || !exists || !winningHead.Valid() ||
		conformanceSnapshotByID(winningHead.ProcessSnapshots(), original.ID()).Status() != agent.StatusCompleted {
		t.Fatalf("winner did not publish a durable terminal head: exists=%t error=%v", exists, err)
	}
	blocking.continueCommit()
	stale, err := original.Await(context.Background())
	assertCrashRuntimeError(t, original, crashAwaitResult{result: stale, err: err}, agent.ErrTreeIncarnationConflict)
	assertCrashHead(t, driver, original.ID(), winningHead.Digest())
	if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if err := originalEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type conformanceDurabilityProbe struct {
	durability agent.TreeDurability

	mu          sync.Mutex
	effects     []agent.EffectBoundaryKind
	checkpoints []agent.TreeCheckpointKind
}

func newConformanceDurabilityProbe(
	t *testing.T,
	durability agent.TreeDurability,
) *conformanceDurabilityProbe {
	t.Helper()
	if durability == nil {
		t.Fatal("TreeDurability conformance driver returned nil")
	}
	return &conformanceDurabilityProbe{durability: durability}
}

func (c *conformanceDurabilityProbe) ActivateTree(
	ctx context.Context,
	activation agent.TreeActivation,
) error {
	return c.retry(func() error { return c.durability.ActivateTree(ctx, activation) })
}

func (c *conformanceDurabilityProbe) CommitEffect(
	ctx context.Context,
	boundary agent.EffectBoundary,
) error {
	err := c.retry(func() error { return c.durability.CommitEffect(ctx, boundary) })
	if err == nil {
		c.mu.Lock()
		c.effects = append(c.effects, boundary.Kind())
		c.mu.Unlock()
	}
	return err
}

func (c *conformanceDurabilityProbe) CommitCheckpoint(
	ctx context.Context,
	checkpoint agent.TreeCheckpoint,
) error {
	err := c.retry(func() error {
		return c.durability.CommitCheckpoint(ctx, checkpoint)
	})
	if err == nil {
		c.mu.Lock()
		c.checkpoints = append(c.checkpoints, checkpoint.Kind())
		c.mu.Unlock()
	}
	return err
}

func (c *conformanceDurabilityProbe) retry(commit func() error) error {
	if err := commit(); err != nil {
		return err
	}
	return commit()
}

func (c *conformanceDurabilityProbe) assertEffectLifecycle(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.effects) != 3 || c.effects[0] != agent.EffectBoundaryPending ||
		c.effects[1] != agent.EffectBoundarySettled ||
		c.effects[2] != agent.EffectBoundaryResolved {
		t.Fatalf("Effect boundary order=%v", c.effects)
	}
	if len(c.checkpoints) == 0 ||
		c.checkpoints[len(c.checkpoints)-1] != agent.TreeCheckpointTerminal {
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
	if !slices.Equal(c.checkpoints, want) {
		t.Fatalf("checkpoint order=%v, want %v", c.checkpoints, want)
	}
}

type conformanceMode uint8

const (
	conformanceModeInvalid conformanceMode = iota
	conformanceModeEffect
	conformanceModeUnknownEffect
	conformanceModePause
)

type conformancePhase uint8

const (
	conformancePhaseInvalid conformancePhase = iota
	conformancePhaseReady
	conformancePhaseAwaitingEffect
	conformancePhaseFinished
)

const (
	conformanceStatusTimeout = 5 * time.Second
	conformancePollInterval  = time.Millisecond
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
	inputSchema, err := agent.SchemaFor[conformanceInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := agent.SchemaFor[conformanceOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name:        "agenttest.durability_conformance",
		Description: "Exercises the complete durable tree commit contract.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &conformanceDefinition{descriptor: descriptor, mode: mode}
	dispatcher := conformanceDispatcher{unknown: mode == conformanceModeUnknownEffect}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		Dispatcher:           dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("agenttest durability implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte{byte(mode)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (c *conformanceDefinition) Descriptor() agent.Descriptor { return c.descriptor }

func (c *conformanceDefinition) Start(input agent.Input) (agent.Execution, error) {
	value, err := input.Decode[conformanceInput]()
	if err != nil {
		return nil, err
	}
	return &conformanceExecution{
		definition: c,
		state:      conformanceState{Phase: conformancePhaseReady, Value: value.Value},
	}, nil
}

func (c *conformanceDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != c.descriptor.Name() {
		return nil, agent.ErrInvalidExecutionState
	}
	var value conformanceState
	if err := json.Unmarshal(state.Payload(), &value); err != nil {
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
	case conformanceModeEffect, conformanceModeUnknownEffect:
		return c.stepEffect(signals)
	case conformanceModePause:
		if c.state.Phase != conformancePhaseReady || len(signals) != 0 {
			return agent.Transition{}, errors.New("agenttest: paused execution cannot advance")
		}
		c.state.Phase = conformancePhaseFinished
		return agent.Pause(0, "durability conformance parked state")
	default:
		return agent.Transition{}, errors.New("agenttest: invalid conformance mode")
	}
}

func (c *conformanceExecution) stepEffect(signals []agent.Signal) (agent.Transition, error) {
	switch c.state.Phase {
	case conformancePhaseReady:
		if len(signals) != 0 {
			return agent.Transition{}, errors.New("agenttest: unexpected initial Signal")
		}
		c.state.Phase = conformancePhaseAwaitingEffect
		payload, err := json.Marshal(conformanceInput{Value: c.state.Value})
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
		if err := json.Unmarshal(signals[0].Payload(), &output); err != nil {
			return agent.Transition{}, err
		}
		c.state.Phase = conformancePhaseFinished
		encoded, err := agent.EncodeOutput(output)
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Complete(1, encoded)
	default:
		return agent.Transition{}, errors.New("agenttest: completed execution cannot advance")
	}
}

func (c *conformanceExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(c.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState(c.definition.descriptor.Name(), payload)
}

func (c conformancePhase) valid() bool {
	return c == conformancePhaseReady || c == conformancePhaseAwaitingEffect ||
		c == conformancePhaseFinished
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
	if err := json.Unmarshal(request.Effect().Payload(), &input); err != nil {
		return agent.Settlement{}, err
	}
	payload, err := json.Marshal(conformanceOutput(input))
	if err != nil {
		return agent.Settlement{}, err
	}
	status := agent.SettlementStatusSucceeded
	if c.unknown {
		status = agent.SettlementStatusUnknown
	}
	return agent.NewSettlement(request.ID(), status, payload)
}

func (conformanceDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}

func waitForConformanceUnknownEffect(
	t *testing.T,
	engine *agent.Engine,
	process *agent.Process,
) agent.EffectID {
	t.Helper()
	deadline := time.NewTimer(conformanceStatusTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(conformancePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			effectIDs := inspectConformanceProcess(t, engine, process).UnknownEffectIDs()
			if len(effectIDs) == 1 {
				return effectIDs[0]
			}
		case <-deadline.C:
			t.Fatal("Process did not expose one Unknown Effect")
		}
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

func waitForConformanceStatus(
	t *testing.T,
	engine *agent.Engine,
	process *agent.Process,
	want agent.Status,
) {
	t.Helper()
	deadline := time.NewTimer(conformanceStatusTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(conformancePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if inspectConformanceProcess(t, engine, process).Status() == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("Process status=%s, want %s", inspectConformanceProcess(t, engine, process).Status(), want)
		}
	}
}

func waitForConformanceHeadStatus(
	t *testing.T,
	driver TreeDurabilityConformanceDriver,
	rootID agent.ProcessID,
	want agent.Status,
) agent.TreeSnapshot {
	t.Helper()
	deadline := time.NewTimer(conformanceStatusTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(conformancePollInterval)
	defer ticker.Stop()
	var lastError error
	for {
		head, exists, err := driver.LoadTree(context.Background(), rootID)
		lastError = err
		if err == nil && exists && head.Valid() {
			root := conformanceSnapshotByID(head.ProcessSnapshots(), rootID)
			if root.Valid() && root.Status() == want {
				return head
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("authoritative root status did not become %s: last error=%v", want, lastError)
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
