package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func runCrashBeforeChildCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	runCrashChildCommit(t, store, crashCommitBefore)
}

func runCrashAfterChildCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	runCrashChildCommit(t, store, crashCommitAfter)
}

func runCrashChildCommit(t *testing.T, store TreeDurabilityConformanceDriver, phase crashCommitPhase) {
	t.Helper()
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitCheckpointChild, phase: phase,
	})
	deployment := newCrashTreeDeployment(t)
	recorder := &ObservationRecorder{}
	engine := newCrashEngine(t, gate, recorder)
	original := startCrashTree(t, engine, deployment)
	observation := gate.await(t)
	childID := crashTreeChildID(t, observation.prospective, original.ID())
	if _, found := engine.Process(childID); found {
		t.Fatal("child was published before its checkpoint acknowledgment")
	}
	for _, event := range recorder.Events() {
		if event.ProcessID() == childID {
			t.Fatal("unacknowledged child published an observation")
		}
	}
	wantHead := observation.previousDigest
	wantProcesses := 1
	if phase == crashCommitAfter {
		wantHead = observation.prospective.Digest()
		wantProcesses = 2
	}
	head := assertCrashHead(t, store, original.ID(), wantHead)
	if len(head.ProcessSnapshots()) != wantProcesses {
		t.Fatalf("child boundary processes=%d want=%d", len(head.ProcessSnapshots()), wantProcesses)
	}
	restoredEngine := newCrashEngine(t, durability, nil)
	root := restoreCrashTree(t, restoredEngine, deployment, head)
	waitForConformanceStatus(t, restoredEngine, root, agent.StatusWaiting)
	child, found := restoredEngine.Process(childID)
	if !found {
		t.Fatal("restoration changed the child identity")
	}
	waitForConformanceStatus(t, restoredEngine, child, agent.StatusWaiting)
	head, found, err := store.LoadTree(t.Context(), original.ID())
	if err != nil || !found || len(head.ProcessSnapshots()) != 2 {
		t.Fatalf("restored child tree exists=%t processes=%d error=%v", found, len(head.ProcessSnapshots()), err)
	}
	wantBudget := agent.Budget{
		Steps: crashTreeChildStepBudget, Effects: crashTreeChildEffectBudget, Signals: crashTreeChildSignalBudget,
	}
	rootSnapshot := conformanceSnapshotByID(head.ProcessSnapshots(), root.ID())
	childSnapshot := conformanceSnapshotByID(head.ProcessSnapshots(), childID)
	var allocation struct {
		ReservedBudget agent.Budget `json:"reserved_child_budget"`
	}
	if decodeErr := json.Unmarshal(rootSnapshot.JSON(), &allocation); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if rootSnapshot.Budget() != original.Budget() || childSnapshot.Budget() != wantBudget ||
		allocation.ReservedBudget != wantBudget {
		t.Fatalf("restoration changed child allocation: parent=%+v child=%+v reserved=%+v",
			rootSnapshot.Budget(), childSnapshot.Budget(), allocation.ReservedBudget)
	}
	waitID, waiting := inspectConformanceProcess(t, restoredEngine, child).WaitID()
	if !waiting {
		t.Fatal("restored child lost its input wait")
	}
	signalID, err := agent.ParseSignalID("signal:child-publication-answer")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(crashTreeOutput{Completed: true})
	if err != nil {
		t.Fatal(err)
	}
	request, err := agent.NewSignalRequest(signalID, waitID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := child.DeliverSignals(t.Context(), request); err != nil || !accepted {
		t.Fatalf("restored child input accepted=%t error=%v", accepted, err)
	}
	rootResult := awaitCrashProcess(t, root)
	childResult := awaitCrashProcess(t, child)
	for _, result := range []agent.Result{rootResult, childResult} {
		output, present := result.Output()
		decoded, err := output.Decode[crashTreeOutput]()
		if result.Status() != agent.StatusCompleted || !present || err != nil || !decoded.Completed {
			t.Fatalf("tree continuation status=%s output=%+v error=%v", result.Status(), decoded, err)
		}
	}
	wantRootUsage := agent.Usage{CommittedSteps: 4, PreparedEffects: 2, AcceptedSignals: 3}
	wantChildUsage := agent.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}
	if rootResult.Usage() != wantRootUsage || childResult.Usage() != wantChildUsage {
		t.Fatalf("recovered consumption root=%+v child=%+v", rootResult.Usage(), childResult.Usage())
	}
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func runCrashAfterSubtreeCancellationCheckpoint(t *testing.T, store TreeDurabilityConformanceDriver) {
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitCheckpointCancellation, phase: crashCommitAfter,
	})
	deployment := newCrashTreeDeployment(t)
	engine := newCrashEngine(t, gate, nil)
	root := startCrashTree(t, engine, deployment)
	waitForConformanceStatus(t, engine, root, agent.StatusWaiting)
	head, found, err := store.LoadTree(t.Context(), root.ID())
	if err != nil || !found {
		t.Fatalf("waiting tree head exists=%t error=%v", found, err)
	}
	childID := crashTreeChildID(t, head, root.ID())
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("waiting child was not published")
	}
	waitForConformanceStatus(t, engine, child, agent.StatusWaiting)
	if err := child.RequestCancellation(t.Context(), crashTreeCancellationReason); err != nil {
		t.Fatal(err)
	}
	observation := gate.await(t)
	head = assertCrashHead(t, store, root.ID(), observation.prospective.Digest())
	if inspectConformanceProcess(t, engine, root).Status() != agent.StatusWaiting || inspectConformanceProcess(t, engine, child).Status() != agent.StatusWaiting {
		t.Fatal("cancellation was published before checkpoint acknowledgment")
	}
	restoredEngine := newCrashEngine(t, durability, nil)
	restoredRoot := restoreCrashTree(t, restoredEngine, deployment, head)
	if result := awaitCrashProcess(t, restoredRoot); result.Status() != agent.StatusCompleted {
		t.Fatalf("restored parent status=%s", result.Status())
	}
	restoredChild, found := restoredEngine.Process(child.ID())
	if !found {
		t.Fatal("restored tree lost canceled child")
	}
	result := awaitCrashProcess(t, restoredChild)
	if result.Status() != agent.StatusCanceled || result.Termination().Cause() != agent.TerminationCauseHostCancellation {
		t.Fatalf("restored child termination=%+v", result.Termination())
	}
	gate.abort()
	awaitCrashRuntimeError(t, root, errSimulatedHostCrash)
	awaitCrashRuntimeError(t, child, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

type crashTreeRole string

const (
	crashTreeRoleInvalid crashTreeRole = ""
	crashTreeRoleRoot    crashTreeRole = "root"
	crashTreeRoleChild   crashTreeRole = "child"
)

func (c crashTreeRole) valid() bool {
	return c == crashTreeRoleRoot || c == crashTreeRoleChild
}

type crashTreePhase string

const (
	crashTreePhaseInvalid          crashTreePhase = ""
	crashTreePhaseReady            crashTreePhase = "ready"
	crashTreePhaseChildStarting    crashTreePhase = "child_starting"
	crashTreePhaseRootWaitOpening  crashTreePhase = "root_wait_opening"
	crashTreePhaseRootWaiting      crashTreePhase = "root_waiting"
	crashTreePhaseChildWaitOpening crashTreePhase = "child_wait_opening"
	crashTreePhaseChildWaiting     crashTreePhase = "child_waiting"
	crashTreePhaseFinished         crashTreePhase = "finished"
)

func (c crashTreePhase) valid() bool {
	switch c {
	case crashTreePhaseReady, crashTreePhaseChildStarting,
		crashTreePhaseRootWaitOpening, crashTreePhaseRootWaiting,
		crashTreePhaseChildWaitOpening, crashTreePhaseChildWaiting,
		crashTreePhaseFinished:
		return true
	default:
		return false
	}
}

const (
	crashTreeDeploymentName        = "agenttest.durability_crash_tree"
	crashTreeDeploymentDescription = "Exercises child publication, input, and cancellation recovery."
	crashTreeImplementationSeed    = "agenttest durability crash tree implementation"
	crashTreeConfigurationSeed     = "agenttest durability crash tree configuration"
	crashTreeChildKey              = "worker"
	crashTreeRootWaitKey           = "child_completion"
	crashTreeChildWaitKey          = "external_input"
	crashTreeCancellationReason    = "cancel waiting child"
	crashTreeWaitPayloadKind       = "external_input"
	crashTreeChildStepBudget       = 3
	crashTreeChildEffectBudget     = 1
	crashTreeChildSignalBudget     = 2
	crashTreeDirectChildDepth      = 1
)

type crashTreeInput struct {
	Role crashTreeRole `json:"role"`
}

type crashTreeOutput struct {
	Completed bool `json:"completed"`
}

type crashTreeState struct {
	Role    crashTreeRole  `json:"role"`
	Phase   crashTreePhase `json:"phase"`
	ChildID string         `json:"child_id,omitempty"`
	WaitID  string         `json:"wait_id,omitempty"`
}

func (c crashTreeState) valid() bool {
	if !c.Role.valid() || !c.Phase.valid() {
		return false
	}
	if c.Role == crashTreeRoleRoot {
		switch c.Phase {
		case crashTreePhaseReady, crashTreePhaseChildStarting:
			return c.ChildID == "" && c.WaitID == ""
		case crashTreePhaseRootWaitOpening:
			_, err := agent.ParseProcessID(c.ChildID)
			return err == nil && c.WaitID == ""
		case crashTreePhaseRootWaiting:
			_, childErr := agent.ParseProcessID(c.ChildID)
			_, waitErr := agent.ParseWaitID(c.WaitID)
			return childErr == nil && waitErr == nil
		case crashTreePhaseFinished:
			return true
		default:
			return false
		}
	}
	switch c.Phase {
	case crashTreePhaseReady, crashTreePhaseChildWaitOpening:
		return c.ChildID == "" && c.WaitID == ""
	case crashTreePhaseChildWaiting:
		_, err := agent.ParseWaitID(c.WaitID)
		return err == nil && c.ChildID == ""
	case crashTreePhaseFinished:
		return true
	default:
		return false
	}
}

type crashTreeDefinition struct {
	descriptor agent.Descriptor
	reference  agent.DeploymentRef
}

func (c *crashTreeDefinition) Descriptor() agent.Descriptor { return c.descriptor }

func (c *crashTreeDefinition) Start(input agent.Input) (agent.Execution, error) {
	decoded, err := input.Decode[crashTreeInput]()
	if err != nil {
		return nil, err
	}
	state := crashTreeState{Role: decoded.Role, Phase: crashTreePhaseReady}
	if !state.valid() {
		return nil, agent.ErrInvalidInput
	}
	return &crashTreeExecution{definition: c, state: state}, nil
}

func (c *crashTreeDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != c.descriptor.Name() {
		return nil, agent.ErrInvalidExecutionState
	}
	var decoded crashTreeState
	if err := json.Unmarshal(state.Payload(), &decoded); err != nil {
		return nil, err
	}
	if !decoded.valid() {
		return nil, agent.ErrInvalidExecutionState
	}
	return &crashTreeExecution{definition: c, state: decoded}, nil
}

type crashTreeExecution struct {
	definition *crashTreeDefinition
	state      crashTreeState
}

func (c *crashTreeExecution) Step(
	_ context.Context,
	signals []agent.Signal,
) (agent.Transition, error) {
	if c.state.Role == crashTreeRoleRoot {
		return c.stepRoot(signals)
	}
	return c.stepChild(signals)
}

func (c *crashTreeExecution) stepRoot(signals []agent.Signal) (agent.Transition, error) {
	switch c.state.Phase {
	case crashTreePhaseReady:
		return c.startRootChild(signals)
	case crashTreePhaseChildStarting:
		return c.openRootChildWait(signals)
	case crashTreePhaseRootWaitOpening:
		return c.enterRootChildWait(signals)
	case crashTreePhaseRootWaiting:
		return c.completeRoot(signals)
	default:
		return agent.Transition{}, errors.New("agenttest: root cannot advance from its current phase")
	}
}

func (c *crashTreeExecution) startRootChild(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 0 {
		return agent.Transition{}, errors.New("agenttest: root received an unexpected initial Signal")
	}
	input, err := c.definition.descriptor.EncodeInput(crashTreeInput{Role: crashTreeRoleChild})
	if err != nil {
		return agent.Transition{}, err
	}
	key, err := agent.ParseChildKey(crashTreeChildKey)
	if err != nil {
		return agent.Transition{}, err
	}
	budget, err := agent.NewBudget(agent.BudgetConfig{Steps: crashTreeChildStepBudget, Effects: crashTreeChildEffectBudget, Signals: crashTreeChildSignalBudget})
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.StartChild(agent.ChildSpec{
		Key: key, DeploymentRef: c.definition.reference, Input: input,
		Budget: budget, Capabilities: agent.CapabilitySet{},
	})
	if err != nil {
		return agent.Transition{}, err
	}
	c.state.Phase = crashTreePhaseChildStarting
	return agent.Continue(0, effect)
}

func (c *crashTreeExecution) openRootChildWait(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 1 {
		return agent.Transition{}, errors.New("agenttest: child start result is missing")
	}
	started, err := agent.ParseChildStartResult(signals[0])
	if err != nil {
		return agent.Transition{}, err
	}
	childID, ok := started.ProcessID()
	if !ok {
		return agent.Transition{}, errors.New("agenttest: child did not start")
	}
	key, err := agent.ParseWaitKey(crashTreeRootWaitKey)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(agent.ChildWaitSpec{
		Boundary: agent.ChildWaitBoundaryResult,
		Key:      key, Children: []agent.ProcessID{childID}, Condition: agent.AllChildren(),
	})
	if err != nil {
		return agent.Transition{}, err
	}
	c.state.ChildID = childID.String()
	c.state.Phase = crashTreePhaseRootWaitOpening
	return agent.Continue(1, effect)
}

func (c *crashTreeExecution) enterRootChildWait(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 1 {
		return agent.Transition{}, errors.New("agenttest: child wait acknowledgement is missing")
	}
	opened, err := agent.ParseChildWaitOpened(signals[0])
	if err != nil {
		return agent.Transition{}, err
	}
	c.state.WaitID = opened.WaitID().String()
	c.state.Phase = crashTreePhaseRootWaiting
	return agent.Wait(1, opened.WaitID())
}

func (c *crashTreeExecution) completeRoot(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 1 {
		return agent.Transition{}, errors.New("agenttest: child completion is missing")
	}
	if _, err := agent.ParseChildWaitSatisfied(signals[0]); err != nil {
		return agent.Transition{}, err
	}
	c.state.Phase = crashTreePhaseFinished
	output, err := agent.EncodeOutput(crashTreeOutput{Completed: true})
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(1, output)
}

func (c *crashTreeExecution) stepChild(signals []agent.Signal) (agent.Transition, error) {
	switch c.state.Phase {
	case crashTreePhaseReady:
		if len(signals) != 0 {
			return agent.Transition{}, errors.New("agenttest: child received an unexpected initial Signal")
		}
		key, err := agent.ParseWaitKey(crashTreeChildWaitKey)
		if err != nil {
			return agent.Transition{}, err
		}
		payload, err := json.Marshal(struct {
			Kind string `json:"kind"`
		}{Kind: crashTreeWaitPayloadKind})
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.RequestWait(key, payload)
		if err != nil {
			return agent.Transition{}, err
		}
		c.state.Phase = crashTreePhaseChildWaitOpening
		return agent.Continue(0, effect)
	case crashTreePhaseChildWaitOpening:
		if len(signals) != 1 {
			return agent.Transition{}, errors.New("agenttest: external wait acknowledgement is missing")
		}
		waitID, addressed := signals[0].WaitID()
		if !addressed {
			return agent.Transition{}, errors.New("agenttest: external wait acknowledgement has no WaitID")
		}
		c.state.WaitID = waitID.String()
		c.state.Phase = crashTreePhaseChildWaiting
		return agent.Wait(1, waitID)
	case crashTreePhaseChildWaiting:
		if len(signals) != 1 {
			return agent.Transition{}, errors.New("agenttest: external wait response is missing")
		}
		c.state.Phase = crashTreePhaseFinished
		output, err := agent.EncodeOutput(crashTreeOutput{Completed: true})
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Complete(1, output)
	default:
		return agent.Transition{}, errors.New("agenttest: child cannot advance from its current phase")
	}
}

func (c *crashTreeExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(c.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState(c.definition.descriptor.Name(), payload)
}

func newCrashTreeDeployment(t *testing.T) agent.Deployment {
	t.Helper()
	inputSchema, err := agent.SchemaFor[crashTreeInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := agent.SchemaFor[crashTreeOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: crashTreeDeploymentName, Description: crashTreeDeploymentDescription,
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &crashTreeDefinition{descriptor: descriptor}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(crashTreeImplementationSeed)),
		ConfigurationDigest:  agent.ComputeDigest([]byte(crashTreeConfigurationSeed)),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.reference = deployment.DeploymentRef()
	return deployment
}

func startCrashTree(
	t *testing.T,
	engine *agent.Engine,
	deployment agent.Deployment,
) *agent.Process {
	t.Helper()
	input, err := deployment.Descriptor().EncodeInput(crashTreeInput{Role: crashTreeRoleRoot})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func crashTreeChildID(
	t *testing.T,
	snapshot agent.TreeSnapshot,
	rootID agent.ProcessID,
) agent.ProcessID {
	t.Helper()
	for _, process := range snapshot.ProcessSnapshots() {
		if process.ProcessID() != rootID &&
			process.Relation().Depth() == crashTreeDirectChildDepth {
			return process.ProcessID()
		}
	}
	t.Fatal("parked tree has no direct child")
	return agent.ProcessID{}
}
