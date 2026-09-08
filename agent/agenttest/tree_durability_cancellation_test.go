package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func TestCrashAfterSubtreeCancellationCheckpoint(t *testing.T) {
	store := NewMemoryTreeDurability()
	gate := newTreeDurabilityCommitGate(t, store, crashCommitPoint{
		kind: crashCommitCheckpointTerminal, phase: crashCommitAfter,
	})
	deployment := newCrashTreeDeployment(t)
	engine := newCrashEngine(t, gate, nil)
	root := startCrashTree(t, engine, deployment)
	waitForConformanceStatus(t, root, agent.StatusWaiting)
	head, found, err := store.LoadTree(t.Context(), root.ID())
	if err != nil || !found {
		t.Fatalf("waiting tree head exists=%t error=%v", found, err)
	}
	childID := crashTreeChildID(t, head, root.ID())
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("waiting child was not published")
	}
	waitForConformanceStatus(t, child, agent.StatusWaiting)
	if err := child.RequestCancellation(t.Context(), crashTreeCancellationReason); err != nil {
		t.Fatal(err)
	}
	observation := gate.await(t)
	head = assertCrashHead(t, store, root.ID(), observation.prospective.Digest())
	if root.Status() != agent.StatusWaiting || child.Status() != agent.StatusWaiting {
		t.Fatal("cancellation was published before checkpoint acknowledgment")
	}
	restoredEngine := newCrashEngine(t, store, nil)
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
	crashTreeDeploymentDescription = "Creates a waiting child tree for cancellation recovery."
	crashTreeImplementationSeed    = "agenttest durability crash tree implementation"
	crashTreeConfigurationSeed     = "agenttest durability crash tree configuration"
	crashTreeChildKey              = "worker"
	crashTreeRootWaitKey           = "child_completion"
	crashTreeChildWaitKey          = "external_input"
	crashTreeCancellationReason    = "cancel waiting child"
	crashTreeWaitPayloadKind       = "external_input"
	crashTreeChildStepBudget       = 2
	crashTreeChildEffectBudget     = 1
	crashTreeChildSignalBudget     = 1
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
		Key: key, Children: []agent.ProcessID{childID}, Condition: agent.AllChildren(),
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
	if _, err := agent.ParseChildrenCompleted(signals[0]); err != nil {
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
