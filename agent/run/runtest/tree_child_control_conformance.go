package runtest

import (
	startrand "crypto/rand"

	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

type childControlOperation string

const (
	childControlInvalid childControlOperation = ""
	childControlSignal  childControlOperation = "signal"
	childControlCancel  childControlOperation = "cancel"
)

// agent owns the framework operation vocabulary and exports no predicate over
// it. Recognizing a control by rebuilding what agent's own constructors emit
// leaves that vocabulary one owner, so a rename cannot make this suite watch
// for an operation the Runtime no longer issues.
var childControlReferences = sync.OnceValue(func() map[string]childControlOperation {
	childID, err := run.ParseProcessID("process:child-control-reference")
	if err != nil {
		panic(fmt.Sprintf("runtest: child control reference ProcessID: %v", err))
	}
	request, err := newChildControlSignalRequest()
	if err != nil {
		panic(fmt.Sprintf("runtest: child control reference SignalRequest: %v", err))
	}
	signal, err := run.NewChildSignalEffect(childID, request)
	if err != nil {
		panic(fmt.Sprintf("runtest: child signal reference Effect: %v", err))
	}
	cancel, err := run.NewChildCancelEffect(childID, childControlCancelReason)
	if err != nil {
		panic(fmt.Sprintf("runtest: child cancel reference Effect: %v", err))
	}
	return map[string]childControlOperation{
		frameworkOperationName(signal): childControlSignal,
		frameworkOperationName(cancel): childControlCancel,
	}
})

func childControlOperationFor(effect run.Effect) childControlOperation {
	name := frameworkOperationName(effect)
	if name == "" {
		return childControlInvalid
	}
	return childControlReferences()[name]
}

func frameworkOperationName(effect run.Effect) string {
	if effect.Target() != run.EffectTargetFramework {
		return ""
	}
	var wire struct {
		Operation string `json:"operation"`
	}
	if err := jsonv2.Unmarshal(effect.Payload(), &wire); err != nil {
		return ""
	}
	return wire.Operation
}

type childControlScenario struct {
	name        string
	operation   childControlOperation
	duplicate   bool
	fullMailbox bool
	unowned     bool
	wantFailure string
	wantKind    run.FailureKind
}

func (c childControlScenario) rejected() bool { return c.wantFailure != "" }

func (c childControlScenario) controlCount() int {
	if c.duplicate {
		return 2
	}
	return 1
}

func (c childControlScenario) assertFailure(t *testing.T, label string, result run.ChildControlResult) {
	t.Helper()
	failure, failed := result.Failure()
	if failed != c.rejected() || failed && (failure.Code() != c.wantFailure || failure.Kind() != c.wantKind) {
		t.Fatalf("%s failure=%s/%s, want %s/%s", label, failure.Kind(), failure.Code(), c.wantKind, c.wantFailure)
	}
}

func runChildControlConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	t.Helper()
	for _, scenario := range []childControlScenario{
		{name: "signal", operation: childControlSignal},
		{name: "duplicate signal", operation: childControlSignal, duplicate: true},
		{name: "full mailbox", operation: childControlSignal, fullMailbox: true, wantFailure: "engine.child.signal.rejected", wantKind: run.FailureKindExecution},
		{name: "unowned signal", operation: childControlSignal, unowned: true, wantFailure: "engine.child.control.not_owned", wantKind: run.FailureKindContract},
		{name: "cancel", operation: childControlCancel},
		{name: "unowned cancel", operation: childControlCancel, unowned: true, wantFailure: "engine.child.control.not_owned", wantKind: run.FailureKindContract},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			runChildControl(t, factory(), scenario)
		})
	}
}

func runChildControl(t *testing.T, driver TreeCommitterConformanceDriver, scenario childControlScenario) {
	probe := &childControlCommitterProbe{
		Store: newConformanceDurabilityProbe(t, driver), reader: driver,
		observed: make(chan childControlCommitObservation, 4),
	}
	deployment, release := newChildControlDeployment(t, scenario)
	engine, root, child, before := startChildControlTree(t, probe, driver, deployment, release, scenario)
	t.Cleanup(func() { closeConformanceProcess(t, engine, root) })
	ids := make(map[run.EffectID]bool, scenario.controlCount())
	for range scenario.controlCount() {
		observation := probe.await(t)
		if observation.err != nil {
			t.Fatalf("framework control commit: %v", observation.err)
		}
		boundary := observation.boundary
		if boundary.Kind() != run.EffectBoundaryKindSettled || ids[boundary.Request().ID()] {
			t.Fatalf("framework control must first settle directly: kind=%s Effect=%s", boundary.Kind(), boundary.Request().ID())
		}
		ids[boundary.Request().ID()] = true
		if !observation.found || !observation.head.Valid() || observation.head.Digest() != boundary.TreeSnapshot().Digest() {
			t.Fatalf("framework control acknowledged head digest=%s exists=%t, want %s", observation.head.Digest(), observation.found, boundary.TreeSnapshot().Digest())
		}
		assertChildControlCut(t, observation.head, boundary, before, child.Relation().ProcessID(), scenario)
	}
	waitForConformanceStatus(t, engine, root, run.StatusPaused)
	assertChildControlContinuation(t, driver, engine, root, child.Relation().ProcessID(), before, scenario)
	select {
	case extra := <-probe.observed:
		t.Fatalf("unexpected framework control boundary: %s", extra.boundary.Kind())
	default:
	}
}

// childControlCommitterProbe reads before returning the acknowledgment, while
// this writer still owns the observed boundary, so a later checkpoint cannot
// hide a missing installation.
type childControlCommitterProbe struct {
	run.Store
	reader   treeSnapshotReader
	observed chan childControlCommitObservation
}

func (c *childControlCommitterProbe) CommitEffect(ctx context.Context, boundary run.EffectBoundary) error {
	err := c.Store.CommitEffect(ctx, boundary)
	if childControlOperationFor(boundary.Request().Effect()) == childControlInvalid {
		return err
	}
	observation := childControlCommitObservation{boundary: boundary, err: err}
	if err == nil {
		observation.head, observation.found, observation.err = c.reader.LoadTree(ctx, boundary.TreeSnapshot().RootID())
	}
	c.observed <- observation
	return err
}

func (c *childControlCommitterProbe) await(t *testing.T) childControlCommitObservation {
	t.Helper()
	return awaitConformanceValue(t, c.observed, "framework control commit was not reached")
}

type childControlCommitObservation struct {
	boundary run.EffectBoundary
	head     run.TreeSnapshot
	found    bool
	err      error
}

func runChildControlCrashConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	t.Helper()
	for _, control := range []struct {
		name      string
		operation childControlOperation
	}{
		{name: "signal", operation: childControlSignal},
		{name: "cancel", operation: childControlCancel},
	} {
		for _, crash := range []struct {
			name  string
			phase crashCommitPhase
		}{
			{name: "before store", phase: crashCommitBefore},
			{name: "stored before acknowledgment", phase: crashCommitAfter},
		} {
			t.Run(control.name+"/"+crash.name, func(t *testing.T) {
				runChildControlCrash(t, factory(), control.operation, crashCommitPoint{kind: childControlCommitKind(control.operation), phase: crash.phase})
			})
		}
	}
}

func runChildControlCrash(
	t *testing.T,
	driver TreeCommitterConformanceDriver,
	operation childControlOperation,
	point crashCommitPoint,
) {
	gate := newTreeCommitterCommitGate(t, newConformanceDurabilityProbe(t, driver), point)
	scenario := childControlScenario{operation: operation}
	deployment, release := newChildControlDeployment(t, scenario)
	engine, root, child, before := startChildControlTree(t, gate, driver, deployment, release, scenario)
	t.Cleanup(func() {
		gate.abort()
		closeConformanceProcess(t, engine, root)
	})
	observation := gate.await(t)
	if observation.boundary.Kind() != run.EffectBoundaryKindSettled ||
		childControlOperationFor(observation.boundary.Request().Effect()) != operation {
		t.Fatal("crash gate missed the exact framework control")
	}
	assertChildControlCut(t, observation.prospective, observation.boundary, before, child.Relation().ProcessID(), scenario)
	assertChildControlUnpublished(t, engine, root.Relation().ProcessID(), child.Relation().ProcessID(), before, observation)
	head := assertCrashHead(t, driver, root.Relation().ProcessID(), observation.durableDigest())
	if point.phase == crashCommitBefore {
		storedChild := conformanceSnapshotByID(head.ProcessSnapshots(), child.Relation().ProcessID())
		priorChild := conformanceSnapshotByID(before.ProcessSnapshots(), child.Relation().ProcessID())
		if !bytes.Equal(storedChild.JSON(), priorChild.JSON()) {
			t.Fatal("uncommitted framework control changed the recipient head")
		}
	} else {
		assertChildControlCut(t, head, observation.boundary, before, child.Relation().ProcessID(), scenario)
	}
	gate.abort()
	awaitCrashRuntimeError(t, root, errSimulatedHostCrash)

	// Reparse the stored bytes and build a fresh Definition and Runtime;
	// recovery cannot borrow the interrupted execution's candidate state.
	head, err := run.ParseTreeSnapshot(head.JSON())
	if err != nil {
		t.Fatal(err)
	}
	restoredDeployment, restoredRelease := newChildControlDeployment(t, scenario)
	close(restoredRelease)
	restoredEngine := newCrashEngine(t, driver, nil)
	restoredRoot := restoreCrashTree(t, restoredEngine, restoredDeployment, head)
	t.Cleanup(func() { closeConformanceProcess(t, restoredEngine, restoredRoot) })
	waitForConformanceStatus(t, restoredEngine, restoredRoot, run.StatusPaused)
	assertChildControlContinuation(t, driver, restoredEngine, restoredRoot, child.Relation().ProcessID(), before, scenario)
	if err := driver.CommitEffect(t.Context(), observation.boundary); !errors.Is(err, run.ErrCommitConflict) && !errors.Is(err, run.ErrTreeIncarnationConflict) {
		t.Fatalf("old control writer was not fenced: %v", err)
	}
}

func assertChildControlUnpublished(
	t *testing.T,
	engine *run.Runtime,
	rootID, childID run.ProcessID,
	before run.TreeSnapshot,
	observation crashCommitObservation,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	inspection, err := engine.InspectTree(ctx, rootID)
	cancel()
	if err != nil || inspection.HeadDigest != observation.previousDigest {
		t.Fatalf("framework control head published before acknowledgment: head=%s error=%v", inspection.HeadDigest, err)
	}
	published, rootFound := inspection.Process(rootID)
	publishedChild, childFound := inspection.Process(childID)
	priorChild := conformanceSnapshotByID(before.ProcessSnapshots(), childID)
	if !rootFound || !childFound || !bytes.Equal(publishedChild.Snapshot.JSON(), priorChild.JSON()) {
		t.Fatal("framework control recipient published before acknowledgment")
	}
	for effectID := range published.Snapshot.Settlements() {
		if effectID == observation.boundary.Request().ID() {
			t.Fatal("framework control settlement published before acknowledgment")
		}
	}
	state, err := published.Snapshot.CommittedExecutionState().Decode[childControlState](childControlDeploymentName)
	if err != nil || len(state.Results) != 0 || state.phase() == childControlParked {
		t.Fatalf("control receipt published before acknowledgment: state=%+v error=%v", state, err)
	}
}

func assertChildControlCut(t *testing.T, head run.TreeSnapshot, boundary run.EffectBoundary, before run.TreeSnapshot, childID run.ProcessID, scenario childControlScenario) {
	t.Helper()
	parent := conformanceSnapshotByID(head.ProcessSnapshots(), head.RootID())
	child := conformanceSnapshotByID(head.ProcessSnapshots(), childID)
	priorChild := conformanceSnapshotByID(before.ProcessSnapshots(), childID)
	settlement, settled := boundary.Settlement()
	wantStatus := run.SettlementStatusSucceeded
	if scenario.rejected() {
		wantStatus = run.SettlementStatusFailed
	}
	if !settled || settlement.Status() != wantStatus {
		t.Fatalf("framework control has no definite receipt: %s", settlement.Status())
	}
	var result run.ChildControlResult
	if err := jsonv2.Unmarshal(settlement.Payload(), &result); err != nil {
		t.Fatalf("framework control receipt is invalid: %v", err)
	}
	if !retainsSettlement(parent, boundary.Request().ID(), settlement) {
		t.Fatal("acknowledged parent is missing the framework control receipt")
	}
	scenario.assertFailure(t, "framework control", result)
	switch {
	case scenario.rejected():
		if !bytes.Equal(child.JSON(), priorChild.JSON()) {
			t.Fatal("rejected framework control partially changed its recipient")
		}
	case scenario.operation == childControlSignal:
		assertChildControlSignal(t, child, childControlSignalRequest(t), priorChild.Usage().AcceptedSignals+1)
	default:
		assertChildControlCancelIntent(t, child, priorChild)
	}
	assertChildControlAllocation(t, parent, child, before)
}

func retainsSettlement(process run.ProcessSnapshot, effectID run.EffectID, settlement run.Settlement) bool {
	for recordedID, recorded := range process.Settlements() {
		if recordedID == effectID && bytes.Equal(recorded.Payload(), settlement.Payload()) {
			return true
		}
	}
	return false
}

func assertChildControlCancelIntent(t *testing.T, child, priorChild run.ProcessSnapshot) {
	t.Helper()
	var intent struct {
		PendingControl struct {
			Cancellation struct {
				Owner  string `json:"owner"`
				Reason string `json:"reason"`
			} `json:"cancellation"`
		} `json:"pending_control"`
	}
	if err := jsonv2.Unmarshal(child.JSON(), &intent); err != nil {
		t.Fatal(err)
	}
	if child.Status().Terminal() || intent.PendingControl.Cancellation.Owner != "parent" || intent.PendingControl.Cancellation.Reason != childControlCancelReason {
		t.Fatalf("cancellation receipt must retain intent before termination: status=%s intent=%+v", child.Status(), intent.PendingControl)
	}
	if child.Usage() != priorChild.Usage() {
		t.Fatal("cancellation receipt changed child consumption")
	}
}

func assertChildControlAllocation(t *testing.T, parent, child run.ProcessSnapshot, before run.TreeSnapshot) {
	t.Helper()
	// The parent's child debit is derived from the retained child grant.
	oldParent := conformanceSnapshotByID(before.ProcessSnapshots(), parent.Relation().ProcessID())
	oldChild := conformanceSnapshotByID(before.ProcessSnapshots(), child.Relation().ProcessID())
	if parent.Budget() != oldParent.Budget() || child.Budget() != oldChild.Budget() {
		t.Fatalf("framework control released or changed child allocation: parent=%+v child=%+v", parent.Budget(), child.Budget())
	}
}

func assertChildControlSignal(t *testing.T, child run.ProcessSnapshot, request run.SignalRequest, wantUsage uint64) {
	t.Helper()
	var matches int
	for _, receipt := range child.SignalReceipts() {
		if !receipt.Matches(request) {
			continue
		}
		matches++
		signal, pending := receipt.PendingSignal()
		if receipt.Consumed() || !pending || !bytes.Equal(signal.Payload(), request.Payload()) {
			t.Fatal("paused child did not retain the exact pending control signal")
		}
	}
	if matches != 1 || child.Usage().AcceptedSignals != wantUsage || child.Status() != run.StatusPaused {
		t.Fatalf("child control signal admission count=%d usage=%+v status=%s", matches, child.Usage(), child.Status())
	}
}

func assertChildControlContinuation(t *testing.T, driver TreeCommitterConformanceDriver, engine *run.Runtime, root *run.Handle[run.Payload], childID run.ProcessID, before run.TreeSnapshot, scenario childControlScenario) {
	t.Helper()
	delivered := !scenario.rejected()
	if delivered && scenario.operation == childControlCancel {
		result := awaitCrashProcess(t, childControlProcess(t, engine, childID, "control recovery lost the child"))
		if result.Termination().Status() != run.StatusCanceled || result.Termination().Cause() != run.TerminationCauseParentCancellation {
			t.Fatalf("control recovery lost parent cancellation: status=%s termination=%+v", result.Termination().Status(), result.Termination())
		}
	}
	head := waitForConformanceHeadStatus(t, driver, root.Relation().ProcessID(), run.StatusPaused)
	parent := conformanceSnapshotByID(head.ProcessSnapshots(), root.Relation().ProcessID())
	child := conformanceSnapshotByID(head.ProcessSnapshots(), childID)
	state, err := parent.CommittedExecutionState().Decode[childControlState](childControlDeploymentName)
	if err != nil || state.phase() != childControlParked {
		t.Fatalf("parent did not adopt the control receipt: %v", err)
	}
	if len(state.Results) != scenario.controlCount() {
		t.Fatalf("parent adopted %d receipts, want %d", len(state.Results), scenario.controlCount())
	}
	for _, result := range state.Results {
		scenario.assertFailure(t, "recovered control", result)
	}
	assertChildControlAllocation(t, parent, child, before)
	if delivered && scenario.operation == childControlSignal {
		priorChild := conformanceSnapshotByID(before.ProcessSnapshots(), childID)
		assertChildControlSignal(t, child, childControlSignalRequest(t), priorChild.Usage().AcceptedSignals+1)
		assertChildControlSignalConsumed(t, driver, engine, root.Relation().ProcessID(), child)
	}
}

func assertChildControlSignalConsumed(
	t *testing.T,
	driver TreeCommitterConformanceDriver,
	engine *run.Runtime,
	rootID run.ProcessID,
	admitted run.ProcessSnapshot,
) {
	t.Helper()
	process := childControlProcess(t, engine, admitted.Relation().ProcessID(), "controlled child disappeared before consumption")
	if err := process.Resume(t.Context()); err != nil {
		t.Fatal(err)
	}
	if result := awaitCrashProcess(t, process); result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("control signal consumption status=%s", result.Termination().Status())
	}
	head := waitForConformanceHeadStatus(t, driver, rootID, run.StatusPaused)
	consumed := conformanceSnapshotByID(head.ProcessSnapshots(), admitted.Relation().ProcessID())
	if consumed.Usage().AcceptedSignals != admitted.Usage().AcceptedSignals || consumed.Usage().CommittedSteps != admitted.Usage().CommittedSteps+1 {
		t.Fatal("control recovery repeated signal admission or consumption")
	}
	receipts := consumed.SignalReceipts()
	if len(receipts) != 1 || !receipts[0].Matches(childControlSignalRequest(t)) || !receipts[0].Consumed() {
		t.Fatal("control consumption did not retain exactly one durable receipt")
	}
	if _, pending := receipts[0].PendingSignal(); pending {
		t.Fatal("control consumption retained an unconsumed payload")
	}
}

func childControlProcess(t *testing.T, engine *run.Runtime, childID run.ProcessID, missing string) *run.Handle[run.Payload] {
	t.Helper()
	process, found := engine.Process(childID)
	if !found {
		t.Fatal(missing)
	}
	return process
}

const (
	childControlDeploymentName = "agenttest.child_control"
	childControlCancelReason   = "parent no longer needs child work"
	childControlChildBudget    = 10
	childControlMailboxSize    = 2
	childControlSignalID       = "signal:framework-control"
	childControlSignalPayload  = `{"instruction":"retain exactly once"}`
)

type childControlPhase string

const (
	childControlReady    childControlPhase = "ready"
	childControlStarting childControlPhase = "starting"
	childControlIssued   childControlPhase = "issued"
	childControlParked   childControlPhase = "parked"
)

// childControlState stores only ready or starting; issued and parked follow
// from what the Process recorded: a parent's started child and adopted
// receipts, or a child's pause.
type childControlState struct {
	Child   bool                     `json:"child"`
	Phase   childControlPhase        `json:"phase"`
	Paused  bool                     `json:"paused,omitzero"`
	ChildID run.ProcessID            `json:"child_id,omitzero"`
	Results []run.ChildControlResult `json:"results"`
}

func (c childControlState) phase() childControlPhase {
	switch {
	case c.Child && c.Paused:
		return childControlParked
	case c.Child || c.Phase != childControlStarting || !c.ChildID.Valid():
		return c.Phase
	case len(c.Results) == 0:
		return childControlIssued
	default:
		return childControlParked
	}
}

// valid admits only states this fixture writes.
func (c childControlState) valid() bool {
	if c.Child {
		return c.Phase == childControlReady && !c.ChildID.Valid() && len(c.Results) == 0
	}
	switch c.Phase {
	case childControlReady:
		return !c.Paused && !c.ChildID.Valid() && len(c.Results) == 0
	case childControlStarting:
		return !c.Paused && (c.ChildID.Valid() || len(c.Results) == 0)
	default:
		return false
	}
}

type childControlDefinition struct {
	descriptor run.Descriptor
	reference  run.ProgramRef
	scenario   childControlScenario
	release    <-chan struct{}
}

func (c *childControlDefinition) Descriptor() run.Descriptor { return c.descriptor }

func (*childControlDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (c *childControlDefinition) Start(input run.Payload) (run.Execution, error) {
	child, err := input.Decode[bool]()
	if err != nil {
		return nil, err
	}
	return &childControlExecution{definition: c, state: childControlState{Child: child, Phase: childControlReady}}, nil
}

func (c *childControlDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[childControlState](c.descriptor.Name())
	if err != nil {
		return nil, err
	}
	if !value.valid() {
		return nil, run.ErrInvalidExecutionState
	}
	return &childControlExecution{definition: c, state: value}, nil
}

type childControlExecution struct {
	definition *childControlDefinition
	state      childControlState
}

func (c *childControlExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if c.state.Child {
		return c.stepChild(signals)
	}
	switch c.state.phase() {
	case childControlReady:
		return c.startChild()
	case childControlStarting:
		return c.issueControl(ctx, signals)
	case childControlIssued:
		return c.adoptReceipts(signals)
	default:
		return run.Transition{}, errors.New("runtest: controlled parent unexpectedly resumed")
	}
}

func (c *childControlExecution) stepChild(signals []run.Signal) (run.Transition, error) {
	if !c.state.Paused {
		c.state.Paused = true
		return run.Pause(0, "hold control input for independent inspection")
	}
	if len(signals) != 1 {
		return run.Transition{}, errors.New("runtest: child did not consume exactly one control signal")
	}
	output, err := run.EncodePayload(true)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(1, output)
}

func (c *childControlExecution) startChild() (run.Transition, error) {
	input, err := c.definition.descriptor.EncodeInput(true)
	if err != nil {
		return run.Transition{}, err
	}
	key, err := run.ParseChildKey("controlled")
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewChildStartEffect(run.ChildSpec{
		Key: key, ProgramRef: c.definition.reference, Input: input,
		Budget: run.Budget{Steps: run.NewQuota(childControlChildBudget), Effects: run.NewQuota(childControlChildBudget), Signals: run.NewQuota(childControlChildBudget)},
	})
	if err != nil {
		return run.Transition{}, err
	}
	c.state.Phase = childControlStarting
	return run.Continue(0, effect)
}

// issueControl waits for the harness release so the suite can capture the
// pre-control head before the Runtime commits the control boundary.
func (c *childControlExecution) issueControl(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if len(signals) != 1 {
		return run.Transition{}, errors.New("runtest: missing controlled child start")
	}
	started, err := signals[0].Frame[run.ChildStartResult]()
	if err != nil {
		return run.Transition{}, err
	}
	childID, found := started.ProcessID()
	if !found {
		return run.Transition{}, errors.New("runtest: controlled child did not start")
	}
	select {
	case <-c.definition.release:
	case <-ctx.Done():
		return run.Transition{}, ctx.Err()
	}
	c.state.ChildID = childID
	effect, err := c.controlEffect()
	if err != nil {
		return run.Transition{}, err
	}
	if c.definition.scenario.duplicate {
		return run.Continue(1, effect, effect)
	}
	return run.Continue(1, effect)
}

func (c *childControlExecution) adoptReceipts(signals []run.Signal) (run.Transition, error) {
	if len(signals) == 0 {
		return run.Transition{}, errors.New("runtest: control receipt is missing")
	}
	for _, signal := range signals {
		result, err := signal.Frame[run.ChildControlResult]()
		if err != nil {
			return run.Transition{}, errors.New("runtest: control receipt is invalid")
		}
		c.state.Results = append(c.state.Results, result)
	}
	return run.Pause(uint32(len(signals)), "control receipt adopted")
}

func (c *childControlExecution) controlEffect() (run.Effect, error) {
	childID := c.state.ChildID
	if c.definition.scenario.unowned {
		var err error
		childID, err = run.ParseProcessID("process:outside-controlled-tree")
		if err != nil {
			return run.Effect{}, err
		}
	}
	if c.definition.scenario.operation == childControlCancel {
		return run.NewChildCancelEffect(childID, childControlCancelReason)
	}
	request, err := newChildControlSignalRequest()
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewChildSignalEffect(childID, request)
}

func (c *childControlExecution) Snapshot() (run.ExecutionState, error) {
	payload, err := jsonv2.Marshal(c.state)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState(c.definition.descriptor.Name(), payload)
}

func newChildControlDeployment(t *testing.T, scenario childControlScenario) (run.Program[run.Payload, run.Payload], chan struct{}) {
	t.Helper()
	inputSchema, err := jsonschema.For[bool]()
	if err != nil {
		t.Fatal(err)
	}
	signalSchema, err := jsonschema.Parse([]byte("true"))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: childControlDeploymentName, Description: "Exercises atomic tree-local child controls.",
		InputSchema: inputSchema, OutputSchema: inputSchema, SignalSchema: signalSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	definition := &childControlDefinition{descriptor: descriptor, scenario: scenario, release: release}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, ImplementationDigest: run.ComputeDigest([]byte(childControlDeploymentName)),
		ConfigurationDigest: run.ComputeDigest(fmt.Appendf(nil, "%s/%t/%t", scenario.operation, scenario.duplicate, scenario.unowned)),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.reference = deployment.ProgramRef()
	return deployment, release
}

func startChildControlTree(t *testing.T, committer run.Store, reader TreeCommitterConformanceDriver, deployment run.Program[run.Payload, run.Payload], release chan struct{}, scenario childControlScenario) (*run.Runtime, *run.Handle[run.Payload], *run.Handle[run.Payload], run.TreeSnapshot) {
	t.Helper()
	engine, err := run.New(context.Background(), run.Config{
		Store:      committer,
		Budget:     run.Budget{Steps: run.NewQuota(100), Effects: run.NewQuota(100), Signals: run.NewQuota(100)},
		TreeLimits: run.TreeLimits{MaxPendingSignals: childControlMailboxSize},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := deployment.Descriptor().EncodeInput(false)
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	child := waitForChildControlChild(t, engine, root)
	waitForConformanceStatus(t, engine, child, run.StatusPaused)
	if scenario.fullMailbox {
		fillChildControlMailbox(t, child)
	}
	before, found, err := reader.LoadTree(t.Context(), root.Relation().ProcessID())
	if err != nil || !found || !before.Valid() {
		t.Fatalf("controlled child head exists=%t error=%v", found, err)
	}
	close(release)
	return engine, root, child, before
}

func fillChildControlMailbox(t *testing.T, child *run.Handle[run.Payload]) {
	t.Helper()
	for index := range childControlMailboxSize {
		id, err := run.ParseSignalID(fmt.Sprintf("signal:occupied:%d", index))
		if err != nil {
			t.Fatal(err)
		}
		request, err := run.NewSignalRequest(id, run.WaitID{}, []byte(`"occupied"`))
		if err != nil {
			t.Fatal(err)
		}
		if accepted, err := child.DeliverSignals(t.Context(), request); err != nil || !accepted {
			t.Fatalf("mailbox fixture admission=%t error=%v", accepted, err)
		}
	}
}

func waitForChildControlChild(t *testing.T, engine *run.Runtime, root *run.Handle[run.Payload]) *run.Handle[run.Payload] {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	ticker := time.NewTicker(conformancePollInterval)
	defer ticker.Stop()
	for {
		inspection, err := engine.InspectTree(ctx, root.Relation().ProcessID())
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range inspection.Processes {
			if process.Snapshot.Relation().ProcessID() == root.Relation().ProcessID() {
				continue
			}
			if child, found := engine.Process(process.Snapshot.Relation().ProcessID()); found {
				return child
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("controlled child was not published", ctx.Err())
			return nil
		}
	}
}

func newChildControlSignalRequest() (run.SignalRequest, error) {
	id, err := run.ParseSignalID(childControlSignalID)
	if err != nil {
		return run.SignalRequest{}, err
	}
	return run.NewSignalRequest(id, run.WaitID{}, []byte(childControlSignalPayload))
}

func childControlSignalRequest(t *testing.T) run.SignalRequest {
	t.Helper()
	request, err := newChildControlSignalRequest()
	if err != nil {
		t.Fatal(err)
	}
	return request
}
