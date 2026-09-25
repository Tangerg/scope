package agenttest

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
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
// for an operation the Engine no longer issues.
var childControlReferences = sync.OnceValue(func() map[string]childControlOperation {
	childID, childErr := agent.ParseProcessID("process:child-control-reference")
	signalID, signalErr := agent.ParseSignalID("signal:child-control-reference")
	if childErr != nil || signalErr != nil {
		return nil
	}
	request, requestErr := agent.NewSignalRequest(signalID, agent.WaitID{}, []byte("{}"))
	if requestErr != nil {
		return nil
	}
	signal, signalEffectErr := agent.NewChildSignalEffect(childID, request)
	cancel, cancelErr := agent.NewChildCancelEffect(childID, childControlCancelReason)
	if signalEffectErr != nil || cancelErr != nil {
		return nil
	}
	return map[string]childControlOperation{
		frameworkOperationName(signal): childControlSignal,
		frameworkOperationName(cancel): childControlCancel,
	}
})

func childControlOperationFor(effect agent.Effect) childControlOperation {
	name := frameworkOperationName(effect)
	if name == "" {
		return childControlInvalid
	}
	return childControlReferences()[name]
}

func frameworkOperationName(effect agent.Effect) string {
	if effect.Target() != agent.EffectTargetFramework {
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
	wantKind    agent.FailureKind
}

func runChildControlConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	t.Helper()
	for _, scenario := range []childControlScenario{
		{name: "signal", operation: childControlSignal},
		{name: "duplicate signal", operation: childControlSignal, duplicate: true},
		{name: "full mailbox", operation: childControlSignal, fullMailbox: true, wantFailure: "engine.child.signal.rejected", wantKind: agent.FailureKindExecution},
		{name: "unowned signal", operation: childControlSignal, unowned: true, wantFailure: "engine.child.control.not_owned", wantKind: agent.FailureKindContract},
		{name: "cancel", operation: childControlCancel},
		{name: "unowned cancel", operation: childControlCancel, unowned: true, wantFailure: "engine.child.control.not_owned", wantKind: agent.FailureKindContract},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			driver := factory()
			probe := &childControlCommitterProbe{
				TreeCommitter: newConformanceDurabilityProbe(t, driver), reader: driver,
				observed: make(chan childControlCommitObservation, 4),
			}
			deployment, release := newChildControlDeployment(t, scenario)
			engine, root, child, before := startChildControlTree(t, probe, driver, deployment, release, scenario)
			t.Cleanup(func() { closeSignalConformanceProcess(t, engine, root) })
			count := 1
			if scenario.duplicate {
				count++
			}
			ids := make(map[agent.EffectID]bool, count)
			for range count {
				observation := probe.await(t)
				if observation.err != nil {
					t.Fatalf("framework control commit: %v", observation.err)
				}
				boundary := observation.boundary
				if boundary.Kind() != agent.EffectBoundaryKindSettled || ids[boundary.Request().ID()] {
					t.Fatalf("framework control must first settle directly: kind=%s Effect=%s", boundary.Kind(), boundary.Request().ID())
				}
				ids[boundary.Request().ID()] = true
				if !observation.found || !observation.head.Valid() || observation.head.Digest() != boundary.TreeSnapshot().Digest() {
					t.Fatalf("framework control acknowledged head digest=%s exists=%t, want %s", observation.head.Digest(), observation.found, boundary.TreeSnapshot().Digest())
				}
				assertChildControlCut(t, observation.head, boundary, before, child.ID(), scenario)
			}
			waitForConformanceStatus(t, engine, root, agent.StatusPaused)
			assertChildControlContinuation(t, driver, engine, root, child.ID(), before, scenario)
			select {
			case extra := <-probe.observed:
				t.Fatalf("unexpected framework control boundary: %s", extra.boundary.Kind())
			default:
			}
		})
	}
}

// Reads occur before returning the acknowledgment, while this writer still owns
// the observed boundary. A later checkpoint must not hide a missing installation.
type childControlCommitterProbe struct {
	agent.TreeCommitter
	reader   treeSnapshotReader
	observed chan childControlCommitObservation
}

func (c *childControlCommitterProbe) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	err := c.TreeCommitter.CommitEffect(ctx, boundary)
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
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	select {
	case observation := <-c.observed:
		return observation
	case <-ctx.Done():
		t.Fatal("framework control commit was not reached", ctx.Err())
		return childControlCommitObservation{}
	}
}

type childControlCommitObservation struct {
	boundary agent.EffectBoundary
	head     agent.TreeSnapshot
	found    bool
	err      error
}

func runChildControlCrashConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	t.Helper()
	for _, control := range []struct {
		name      string
		operation childControlOperation
		kind      crashCommitKind
	}{
		{name: "signal", operation: childControlSignal, kind: crashCommitChildSignal},
		{name: "cancel", operation: childControlCancel, kind: crashCommitChildCancel},
	} {
		for _, crash := range []struct {
			name  string
			phase crashCommitPhase
		}{
			{name: "before store", phase: crashCommitBefore},
			{name: "stored before acknowledgment", phase: crashCommitAfter},
		} {
			t.Run(control.name+"/"+crash.name, func(t *testing.T) {
				driver := factory()
				gate := newTreeCommitterCommitGate(t, newConformanceDurabilityProbe(t, driver), crashCommitPoint{
					kind: control.kind, phase: crash.phase,
				})
				scenario := childControlScenario{operation: control.operation}
				deployment, release := newChildControlDeployment(t, scenario)
				engine, root, child, before := startChildControlTree(t, gate, driver, deployment, release, scenario)
				t.Cleanup(func() {
					gate.abort()
					closeSignalConformanceProcess(t, engine, root)
				})
				observation := gate.await(t)
				if observation.boundary.Kind() != agent.EffectBoundaryKindSettled ||
					childControlOperationFor(observation.boundary.Request().Effect()) != control.operation {
					t.Fatal("crash gate missed the exact framework control")
				}
				assertChildControlCut(t, observation.prospective, observation.boundary, before, child.ID(), scenario)
				inspectCtx, cancelInspection := context.WithTimeout(t.Context(), conformanceStatusTimeout)
				inspection, err := engine.InspectTree(inspectCtx, root.ID())
				cancelInspection()
				if err != nil || inspection.HeadDigest != observation.previousDigest {
					t.Fatalf("framework control head published before acknowledgment: head=%s error=%v", inspection.HeadDigest, err)
				}
				published, rootFound := inspection.Process(root.ID())
				publishedChild, childFound := inspection.Process(child.ID())
				priorChild := conformanceSnapshotByID(before.ProcessSnapshots(), child.ID())
				if !rootFound || !childFound || !bytes.Equal(publishedChild.Snapshot.JSON(), priorChild.JSON()) {
					t.Fatal("framework control recipient published before acknowledgment")
				}
				for _, settlement := range published.Snapshot.Settlements() {
					if settlement.EffectID() == observation.boundary.Request().ID() {
						t.Fatal("framework control settlement published before acknowledgment")
					}
				}
				state, err := published.Snapshot.CommittedExecutionState().Decode[childControlState](childControlDeploymentName)
				if err != nil || len(state.Results) != 0 || state.Phase == childControlParked {
					t.Fatalf("control receipt published before acknowledgment: state=%+v error=%v", state, err)
				}
				want := observation.previousDigest
				if crash.phase == crashCommitAfter {
					want = observation.prospective.Digest()
				}
				head := assertCrashHead(t, driver, root.ID(), want)
				if crash.phase == crashCommitBefore {
					storedChild := conformanceSnapshotByID(head.ProcessSnapshots(), child.ID())
					priorChild := conformanceSnapshotByID(before.ProcessSnapshots(), child.ID())
					if !bytes.Equal(storedChild.JSON(), priorChild.JSON()) {
						t.Fatal("uncommitted framework control changed the recipient head")
					}
				} else {
					assertChildControlCut(t, head, observation.boundary, before, child.ID(), scenario)
				}
				gate.abort()
				awaitCrashRuntimeError(t, root, errSimulatedHostCrash)
				// Reparse the stored bytes and build a fresh Definition and Engine;
				// recovery cannot borrow the interrupted execution's candidate state.
				head, err = agent.ParseTreeSnapshot(head.JSON())
				if err != nil {
					t.Fatal(err)
				}
				restoredDeployment, restoredRelease := newChildControlDeployment(t, scenario)
				close(restoredRelease)
				restoredEngine := newCrashEngine(t, driver, nil)
				restoredRoot := restoreCrashTree(t, restoredEngine, restoredDeployment, head)
				t.Cleanup(func() { closeSignalConformanceProcess(t, restoredEngine, restoredRoot) })
				waitForConformanceStatus(t, restoredEngine, restoredRoot, agent.StatusPaused)
				assertChildControlContinuation(t, driver, restoredEngine, restoredRoot, child.ID(), before, scenario)
				if err := driver.CommitEffect(t.Context(), observation.boundary); !errors.Is(err, agent.ErrCommitConflict) && !errors.Is(err, agent.ErrTreeIncarnationConflict) {
					t.Fatalf("old control writer was not fenced: %v", err)
				}
			})
		}
	}
}

func assertChildControlCut(t *testing.T, head agent.TreeSnapshot, boundary agent.EffectBoundary, before agent.TreeSnapshot, childID agent.ProcessID, scenario childControlScenario) {
	t.Helper()
	parent := conformanceSnapshotByID(head.ProcessSnapshots(), head.RootID())
	child := conformanceSnapshotByID(head.ProcessSnapshots(), childID)
	priorChild := conformanceSnapshotByID(before.ProcessSnapshots(), childID)
	settlement, settled := boundary.Settlement()
	wantStatus := agent.SettlementStatusSucceeded
	if scenario.wantFailure != "" {
		wantStatus = agent.SettlementStatusFailed
	}
	if !settled || settlement.Status() != wantStatus {
		t.Fatalf("framework control has no definite receipt: %s", settlement.Status())
	}
	var result agent.ChildControlResult
	if err := jsonv2.Unmarshal(settlement.Payload(), &result); err != nil || !result.Matches(boundary.Request().Effect()) {
		t.Fatalf("framework control receipt does not match its request: %v", err)
	}
	retained := false
	for _, recorded := range parent.Settlements() {
		if recorded.EffectID() == settlement.EffectID() && bytes.Equal(recorded.Payload(), settlement.Payload()) {
			retained = true
		}
	}
	if !retained {
		t.Fatal("acknowledged parent is missing the framework control receipt")
	}
	failure, failed := result.Failure()
	if failed != (scenario.wantFailure != "") || failed && (failure.Code() != scenario.wantFailure || failure.Kind() != scenario.wantKind) {
		t.Fatalf("framework control failure=%s/%s, want %s/%s", failure.Kind(), failure.Code(), scenario.wantKind, scenario.wantFailure)
	}
	if failed {
		if !bytes.Equal(child.JSON(), priorChild.JSON()) {
			t.Fatal("rejected framework control partially changed its recipient")
		}
	} else if scenario.operation == childControlSignal {
		assertChildControlSignal(t, child, childControlSignalRequest(t), priorChild.Usage().AcceptedSignals+1)
	} else {
		var intent struct {
			PendingControl struct {
				Owner  string `json:"cancellation_owner"`
				Reason string `json:"cancellation_reason"`
			} `json:"pending_control"`
		}
		if err := jsonv2.Unmarshal(child.JSON(), &intent); err != nil {
			t.Fatal(err)
		}
		if child.Status().Terminal() || intent.PendingControl.Owner != "parent" || intent.PendingControl.Reason != childControlCancelReason {
			t.Fatalf("cancellation receipt must retain intent before termination: status=%s intent=%+v", child.Status(), intent.PendingControl)
		}
		if child.Usage() != priorChild.Usage() {
			t.Fatal("cancellation receipt changed child consumption")
		}
	}
	assertChildControlAllocation(t, parent, child, before)
}

func assertChildControlAllocation(t *testing.T, parent, child agent.ProcessSnapshot, before agent.TreeSnapshot) {
	t.Helper()
	var current, prior struct {
		Allocated struct {
			Steps   uint64 `json:"steps"`
			Effects uint64 `json:"effects"`
			Signals uint64 `json:"signals"`
		} `json:"allocated_resources"`
	}
	oldParent := conformanceSnapshotByID(before.ProcessSnapshots(), parent.ProcessID())
	oldChild := conformanceSnapshotByID(before.ProcessSnapshots(), child.ProcessID())
	if err := jsonv2.Unmarshal(parent.JSON(), &current); err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(oldParent.JSON(), &prior); err != nil {
		t.Fatal(err)
	}
	if current.Allocated != prior.Allocated || current.Allocated.Steps != childControlChildBudget ||
		current.Allocated.Effects != childControlChildBudget || current.Allocated.Signals != childControlChildBudget ||
		parent.Budget() != oldParent.Budget() || child.Budget() != oldChild.Budget() {
		t.Fatalf("framework control released or changed child allocation: current=%+v prior=%+v", current, prior)
	}
}

func assertChildControlSignal(t *testing.T, child agent.ProcessSnapshot, request agent.SignalRequest, wantUsage uint64) {
	t.Helper()
	var matches int
	for _, receipt := range child.SignalReceipts() {
		if receipt.Matches(request) {
			matches++
			signal, pending := receipt.PendingSignal()
			if receipt.Consumed() || !pending || !bytes.Equal(signal.Payload(), request.Payload()) {
				t.Fatal("paused child did not retain the exact pending control signal")
			}
		}
	}
	if matches != 1 || child.Usage().AcceptedSignals != wantUsage || child.Status() != agent.StatusPaused {
		t.Fatalf("child control signal admission count=%d usage=%+v status=%s", matches, child.Usage(), child.Status())
	}
}

func assertChildControlContinuation(t *testing.T, driver TreeCommitterConformanceDriver, engine *agent.Engine, root *agent.Process, childID agent.ProcessID, before agent.TreeSnapshot, scenario childControlScenario) {
	t.Helper()
	if scenario.operation == childControlCancel && scenario.wantFailure == "" {
		child, found := engine.Process(childID)
		if !found {
			t.Fatal("control recovery lost the child")
		}
		result := awaitCrashProcess(t, child)
		if result.Status() != agent.StatusCanceled || result.Termination().Cause() != agent.TerminationCauseParentCancellation {
			t.Fatalf("control recovery lost parent cancellation: status=%s termination=%+v", result.Status(), result.Termination())
		}
	}
	head := waitForConformanceHeadStatus(t, driver, root.ID(), agent.StatusPaused)
	parent := conformanceSnapshotByID(head.ProcessSnapshots(), root.ID())
	child := conformanceSnapshotByID(head.ProcessSnapshots(), childID)
	state, err := parent.CommittedExecutionState().Decode[childControlState](childControlDeploymentName)
	if err != nil || state.Phase != childControlParked {
		t.Fatalf("parent did not adopt the control receipt: %v", err)
	}
	want := 1
	if scenario.duplicate {
		want++
	}
	if len(state.Results) != want {
		t.Fatalf("parent adopted %d receipts, want %d", len(state.Results), want)
	}
	for _, result := range state.Results {
		failure, failed := result.Failure()
		if failed != (scenario.wantFailure != "") || failed && (failure.Code() != scenario.wantFailure || failure.Kind() != scenario.wantKind) {
			t.Fatalf("recovered control failure=%s/%s, want %s/%s", failure.Kind(), failure.Code(), scenario.wantKind, scenario.wantFailure)
		}
	}
	if scenario.operation == childControlSignal && scenario.wantFailure == "" {
		priorChild := conformanceSnapshotByID(before.ProcessSnapshots(), childID)
		assertChildControlSignal(t, child, childControlSignalRequest(t), priorChild.Usage().AcceptedSignals+1)
	}
	assertChildControlAllocation(t, parent, child, before)
	if scenario.operation == childControlSignal && scenario.wantFailure == "" {
		process, found := engine.Process(childID)
		if !found {
			t.Fatal("controlled child disappeared before consumption")
		}
		if err := process.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		if result := awaitCrashProcess(t, process); result.Status() != agent.StatusCompleted {
			t.Fatalf("control signal consumption status=%s", result.Status())
		}
		head = waitForConformanceHeadStatus(t, driver, root.ID(), agent.StatusPaused)
		consumed := conformanceSnapshotByID(head.ProcessSnapshots(), childID)
		if consumed.Usage().AcceptedSignals != child.Usage().AcceptedSignals || consumed.Usage().CommittedSteps != child.Usage().CommittedSteps+1 {
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
}

const (
	childControlDeploymentName = "agenttest.child_control"
	childControlCancelReason   = "parent no longer needs child work"
	childControlChildBudget    = 10
)

type childControlPhase string

const (
	childControlReady    childControlPhase = "ready"
	childControlStarting childControlPhase = "starting"
	childControlIssued   childControlPhase = "issued"
	childControlParked   childControlPhase = "parked"
)

type childControlState struct {
	Child   bool                       `json:"child"`
	Phase   childControlPhase          `json:"phase"`
	ChildID agent.ProcessID            `json:"child_id,omitzero"`
	Results []agent.ChildControlResult `json:"results"`
}

type childControlDefinition struct {
	descriptor agent.Descriptor
	reference  agent.DeploymentRef
	scenario   childControlScenario
	release    <-chan struct{}
}

func (c *childControlDefinition) Descriptor() agent.Descriptor { return c.descriptor }

func (c *childControlDefinition) Start(input agent.Payload) (agent.Execution, error) {
	child, err := input.Decode[bool]()
	if err != nil {
		return nil, err
	}
	return &childControlExecution{definition: c, state: childControlState{Child: child, Phase: childControlReady}}, nil
}

func (c *childControlDefinition) Restore(_ context.Context, state agent.ExecutionState) (agent.Execution, error) {
	value, err := state.Decode[childControlState](c.descriptor.Name())
	if err != nil {
		return nil, err
	}
	switch value.Phase {
	case childControlReady, childControlStarting, childControlIssued, childControlParked:
	default:
		return nil, agent.ErrInvalidExecutionState
	}
	return &childControlExecution{definition: c, state: value}, nil
}

type childControlExecution struct {
	definition *childControlDefinition
	state      childControlState
}

func (c *childControlExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if c.state.Child {
		if c.state.Phase == childControlParked {
			if len(signals) != 1 {
				return agent.Transition{}, errors.New("agenttest: child did not consume exactly one control signal")
			}
			output, err := agent.EncodePayload(true)
			if err != nil {
				return agent.Transition{}, err
			}
			return agent.Complete(1, output)
		}
		c.state.Phase = childControlParked
		return agent.Pause(0, "hold control input for independent inspection")
	}
	switch c.state.Phase {
	case childControlReady:
		input, err := c.definition.descriptor.EncodeInput(true)
		if err != nil {
			return agent.Transition{}, err
		}
		key, err := agent.ParseChildKey("controlled")
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewChildStartEffect(agent.ChildSpec{
			Key: key, DeploymentRef: c.definition.reference, Input: input,
			Budget: agent.Budget{Steps: agent.NewQuota(childControlChildBudget), Effects: agent.NewQuota(childControlChildBudget), Signals: agent.NewQuota(childControlChildBudget)},
		})
		if err != nil {
			return agent.Transition{}, err
		}
		c.state.Phase = childControlStarting
		return agent.Continue(0, effect)
	case childControlStarting:
		if len(signals) != 1 {
			return agent.Transition{}, errors.New("agenttest: missing controlled child start")
		}
		started, err := agent.ParseChildStartResult(signals[0])
		if err != nil {
			return agent.Transition{}, err
		}
		childID, found := started.ProcessID()
		if !found {
			return agent.Transition{}, errors.New("agenttest: controlled child did not start")
		}
		select {
		case <-c.definition.release:
		case <-ctx.Done():
			return agent.Transition{}, ctx.Err()
		}
		c.state.ChildID = childID
		effect, err := c.controlEffect()
		if err != nil {
			return agent.Transition{}, err
		}
		c.state.Phase = childControlIssued
		if c.definition.scenario.duplicate {
			return agent.Continue(1, effect, effect)
		}
		return agent.Continue(1, effect)
	case childControlIssued:
		effect, err := c.controlEffect()
		if err != nil {
			return agent.Transition{}, err
		}
		for _, signal := range signals {
			result, err := agent.ParseChildControlResult(signal)
			if err != nil || !result.Matches(effect) {
				return agent.Transition{}, errors.New("agenttest: control receipt did not match its request")
			}
			c.state.Results = append(c.state.Results, result)
		}
		c.state.Phase = childControlParked
		return agent.Pause(uint32(len(signals)), "control receipt adopted")
	default:
		return agent.Transition{}, errors.New("agenttest: controlled parent unexpectedly resumed")
	}
}

func (c *childControlExecution) controlEffect() (agent.Effect, error) {
	childID := c.state.ChildID
	if c.definition.scenario.unowned {
		var err error
		childID, err = agent.ParseProcessID("process:outside-controlled-tree")
		if err != nil {
			return agent.Effect{}, err
		}
	}
	if c.definition.scenario.operation == childControlCancel {
		return agent.NewChildCancelEffect(childID, childControlCancelReason)
	}
	id, err := agent.ParseSignalID("signal:framework-control")
	if err != nil {
		return agent.Effect{}, err
	}
	request, err := agent.NewSignalRequest(id, agent.WaitID{}, []byte(`{"instruction":"retain exactly once"}`))
	if err != nil {
		return agent.Effect{}, err
	}
	return agent.NewChildSignalEffect(childID, request)
}

func (c *childControlExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := jsonv2.Marshal(c.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.ParseExecutionState(c.definition.descriptor.Name(), payload)
}

func newChildControlDeployment(t *testing.T, scenario childControlScenario) (agent.Deployment, chan struct{}) {
	t.Helper()
	inputSchema, err := agent.SchemaFor[bool]()
	if err != nil {
		t.Fatal(err)
	}
	signalSchema, err := agent.ParseSchema([]byte("true"))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: childControlDeploymentName, Description: "Exercises atomic tree-local child controls.",
		InputSchema: inputSchema, OutputSchema: inputSchema, SignalSchema: signalSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	definition := &childControlDefinition{descriptor: descriptor, scenario: scenario, release: release}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, ImplementationDigest: agent.ComputeDigest([]byte(childControlDeploymentName)),
		ConfigurationDigest: agent.ComputeDigest(fmt.Appendf(nil, "%s/%t/%t", scenario.operation, scenario.duplicate, scenario.unowned)),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.reference = deployment.DeploymentRef()
	return deployment, release
}

func startChildControlTree(t *testing.T, committer agent.TreeCommitter, reader TreeCommitterConformanceDriver, deployment agent.Deployment, release chan struct{}, scenario childControlScenario) (*agent.Engine, *agent.Process, *agent.Process, agent.TreeSnapshot) {
	t.Helper()
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeCommitter: committer,
		Limits:        agent.Limits{MaxPendingSignals: 2, Budget: agent.Budget{Steps: agent.NewQuota(100), Effects: agent.NewQuota(100), Signals: agent.NewQuota(100)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := deployment.Descriptor().EncodeInput(false)
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	child := waitForChildControlChild(t, engine, root)
	waitForConformanceStatus(t, engine, child, agent.StatusPaused)
	if scenario.fullMailbox {
		for index := range 2 {
			id, parseErr := agent.ParseSignalID(fmt.Sprintf("signal:occupied:%d", index))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			request, requestErr := agent.NewSignalRequest(id, agent.WaitID{}, []byte(`"occupied"`))
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			if accepted, deliveryErr := child.DeliverSignals(t.Context(), request); deliveryErr != nil || !accepted {
				t.Fatalf("mailbox fixture admission=%t error=%v", accepted, deliveryErr)
			}
		}
	}
	before, found, err := reader.LoadTree(t.Context(), root.ID())
	if err != nil || !found || !before.Valid() {
		t.Fatalf("controlled child head exists=%t error=%v", found, err)
	}
	close(release)
	return engine, root, child, before
}

func waitForChildControlChild(t *testing.T, engine *agent.Engine, root *agent.Process) *agent.Process {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	ticker := time.NewTicker(conformancePollInterval)
	defer ticker.Stop()
	for {
		inspection, err := engine.InspectTree(ctx, root.ID())
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range inspection.Processes {
			if process.Snapshot.ProcessID() != root.ID() {
				child, found := engine.Process(process.Snapshot.ProcessID())
				if found {
					return child
				}
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

func childControlSignalRequest(t *testing.T) agent.SignalRequest {
	t.Helper()
	id, err := agent.ParseSignalID("signal:framework-control")
	if err != nil {
		t.Fatal(err)
	}
	request, err := agent.NewSignalRequest(id, agent.WaitID{}, []byte(`{"instruction":"retain exactly once"}`))
	if err != nil {
		t.Fatal(err)
	}
	return request
}
