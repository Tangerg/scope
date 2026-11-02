package runtest

import (
	"context"
	startrand "crypto/rand"

	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func runCrashBeforeChildCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	runCrashChildCommit(t, store, crashCommitBefore)
}

func runCrashAfterChildCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	runCrashChildCommit(t, store, crashCommitAfter)
}

func runCrashChildCommit(t *testing.T, store TreeCommitterConformanceDriver, phase crashCommitPhase) {
	t.Helper()
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitCheckpointChild, phase: phase,
	})
	deployment := newCrashTreeDeployment(t)
	recorder := &ObservationRecorder{}
	engine := newCrashEngine(t, gate, recorder)
	original := startCrashTree(t, engine, deployment)
	observation := gate.await(t)
	childID := crashTreeChildID(t, observation.prospective, original.Relation().ProcessID())
	if _, found := engine.Process(childID); found {
		t.Fatal("child was published before its checkpoint acknowledgment")
	}
	if err := engine.FlushObservations(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, event := range recorder.Events() {
		if event.Relation().ProcessID() == childID {
			t.Fatal("unacknowledged child published an observation")
		}
	}
	wantProcesses := 1
	if phase == crashCommitAfter {
		wantProcesses = 2
	}
	head := assertCrashHead(t, store, original.Relation().ProcessID(), observation.durableDigest())
	if len(head.ProcessSnapshots()) != wantProcesses {
		t.Fatalf("child boundary processes=%d want=%d", len(head.ProcessSnapshots()), wantProcesses)
	}

	restoredEngine := newCrashEngine(t, store, nil)
	root := restoreCrashTree(t, restoredEngine, deployment, head)
	waitForConformanceStatus(t, restoredEngine, root, run.StatusWaiting)
	child, found := restoredEngine.Process(childID)
	if !found {
		t.Fatal("restoration changed the child identity")
	}
	waitForConformanceStatus(t, restoredEngine, child, run.StatusWaiting)
	assertCrashTreeAllocation(t, store, original, childID)
	answerCrashTreeChild(t, restoredEngine, child)
	assertCrashTreeCompleted(t, root, child)
	gate.abort()
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func assertCrashTreeAllocation(
	t *testing.T,
	store TreeCommitterConformanceDriver,
	original *run.Handle[run.Payload],
	childID run.ProcessID,
) {
	t.Helper()
	head, found, err := store.LoadTree(t.Context(), original.Relation().ProcessID())
	if err != nil || !found || len(head.ProcessSnapshots()) != 2 {
		t.Fatalf("restored child tree exists=%t processes=%d error=%v", found, len(head.ProcessSnapshots()), err)
	}
	rootSnapshot := conformanceSnapshotByID(head.ProcessSnapshots(), original.Relation().ProcessID())
	childSnapshot := conformanceSnapshotByID(head.ProcessSnapshots(), childID)
	// The parent's child debit is derived from the retained child grant.
	if rootSnapshot.Budget() != original.Budget() || childSnapshot.Budget() != crashTreeChildBudget() {
		t.Fatalf("restoration changed child allocation: parent=%+v child=%+v",
			rootSnapshot.Budget(), childSnapshot.Budget())
	}
}

func answerCrashTreeChild(t *testing.T, engine *run.Runtime, child *run.Handle[run.Payload]) {
	t.Helper()
	waitID, waiting := inspectConformanceProcess(t, engine, child).WaitID()
	if !waiting {
		t.Fatal("restored child lost its input wait")
	}
	signalID, err := run.ParseSignalID("signal:child-publication-answer")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := jsonv2.Marshal(crashTreeOutput{Completed: true})
	if err != nil {
		t.Fatal(err)
	}
	request, err := run.NewSignalRequest(signalID, waitID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := child.DeliverSignals(t.Context(), request); err != nil || !accepted {
		t.Fatalf("restored child input accepted=%t error=%v", accepted, err)
	}
}

func assertCrashTreeCompleted(t *testing.T, root, child *run.Handle[run.Payload]) {
	t.Helper()
	rootResult := awaitCrashProcess(t, root)
	childResult := awaitCrashProcess(t, child)
	for _, result := range []run.Result[run.Payload]{rootResult, childResult} {
		output, present := result.Termination().Output()
		decoded, err := output.Decode[crashTreeOutput]()
		if result.Termination().Status() != run.StatusCompleted || !present || err != nil || !decoded.Completed {
			t.Fatalf("tree continuation status=%s output=%+v error=%v", result.Termination().Status(), decoded, err)
		}
	}
	wantRootUsage := run.Usage{CommittedSteps: 4, PreparedEffects: 2, AcceptedSignals: 3}
	wantChildUsage := run.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}
	if rootResult.Usage() != wantRootUsage || childResult.Usage() != wantChildUsage {
		t.Fatalf("recovered consumption root=%+v child=%+v", rootResult.Usage(), childResult.Usage())
	}
}

func runCrashAfterSubtreeCancellationCheckpoint(t *testing.T, store TreeCommitterConformanceDriver) {
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitCheckpointCancellation, phase: crashCommitAfter,
	})
	deployment := newCrashTreeDeployment(t)
	engine := newCrashEngine(t, gate, nil)
	root := startCrashTree(t, engine, deployment)
	waitForConformanceStatus(t, engine, root, run.StatusWaiting)
	head, found, err := store.LoadTree(t.Context(), root.Relation().ProcessID())
	if err != nil || !found {
		t.Fatalf("waiting tree head exists=%t error=%v", found, err)
	}
	childID := crashTreeChildID(t, head, root.Relation().ProcessID())
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("waiting child was not published")
	}
	waitForConformanceStatus(t, engine, child, run.StatusWaiting)
	if err := child.Cancel(t.Context(), crashTreeCancellationReason); err != nil {
		t.Fatal(err)
	}
	observation := gate.await(t)
	head = assertCrashHead(t, store, root.Relation().ProcessID(), observation.durableDigest())
	if inspectConformanceProcess(t, engine, root).Status() != run.StatusWaiting || inspectConformanceProcess(t, engine, child).Status() != run.StatusWaiting {
		t.Fatal("cancellation was published before checkpoint acknowledgment")
	}
	restoredEngine := newCrashEngine(t, store, nil)
	restoredRoot := restoreCrashTree(t, restoredEngine, deployment, head)
	if result := awaitCrashProcess(t, restoredRoot); result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("restored parent status=%s", result.Termination().Status())
	}
	restoredChild, found := restoredEngine.Process(child.Relation().ProcessID())
	if !found {
		t.Fatal("restored tree lost canceled child")
	}
	result := awaitCrashProcess(t, restoredChild)
	if result.Termination().Status() != run.StatusCanceled || result.Termination().Cause() != run.TerminationCauseHostCancellation {
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

const (
	crashTreeDeploymentName        = "agenttest.committer_crash_tree"
	crashTreeDeploymentDescription = "Exercises child publication, input, and cancellation recovery."
	crashTreeImplementationSeed    = "agenttest committer crash tree implementation"
	crashTreeConfigurationSeed     = "agenttest committer crash tree configuration"
	crashTreeChildKey              = "worker"
	crashTreeRootWaitKey           = "child_completion"
	crashTreeChildWaitKey          = "external_input"
	crashTreeCancellationReason    = "cancel waiting child"
	crashTreeChildStepBudget       = 3
	crashTreeChildEffectBudget     = 1
	crashTreeChildSignalBudget     = 2
	crashTreeDirectChildDepth      = 1
)

func crashTreeChildBudget() run.Budget {
	return run.Budget{
		Steps:   run.NewQuota(crashTreeChildStepBudget),
		Effects: run.NewQuota(crashTreeChildEffectBudget),
		Signals: run.NewQuota(crashTreeChildSignalBudget),
	}
}

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

// phase derives the wait progress a stored phase reached from the identities
// it produced, so a started child and an opened wait are never recorded twice.
func (c crashTreeState) phase() crashTreePhase {
	switch {
	case c.Phase == crashTreePhaseChildStarting && c.WaitID != "":
		return crashTreePhaseRootWaiting
	case c.Phase == crashTreePhaseChildStarting && c.ChildID != "":
		return crashTreePhaseRootWaitOpening
	case c.Phase == crashTreePhaseChildWaitOpening && c.WaitID != "":
		return crashTreePhaseChildWaiting
	default:
		return c.Phase
	}
}

func (c crashTreeState) valid() bool {
	switch c.Role {
	case crashTreeRoleRoot:
		return c.validRoot()
	case crashTreeRoleChild:
		return c.validChild()
	default:
		return false
	}
}

func (c crashTreeState) validRoot() bool {
	switch c.Phase {
	case crashTreePhaseReady:
		return c.ChildID == "" && c.WaitID == ""
	case crashTreePhaseChildStarting:
		if c.ChildID == "" {
			return c.WaitID == ""
		}
		_, childErr := run.ParseProcessID(c.ChildID)
		_, waitErr := run.ParseWaitID(c.WaitID)
		return childErr == nil && (c.WaitID == "" || waitErr == nil)
	case crashTreePhaseFinished:
		return true
	default:
		return false
	}
}

func (c crashTreeState) validChild() bool {
	switch c.Phase {
	case crashTreePhaseReady:
		return c.ChildID == "" && c.WaitID == ""
	case crashTreePhaseChildWaitOpening:
		_, err := run.ParseWaitID(c.WaitID)
		return c.ChildID == "" && (c.WaitID == "" || err == nil)
	case crashTreePhaseFinished:
		return true
	default:
		return false
	}
}

type crashTreeDefinition struct {
	descriptor run.Descriptor
	reference  run.ProgramRef
}

func (c *crashTreeDefinition) Descriptor() run.Descriptor { return c.descriptor }

func (*crashTreeDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (c *crashTreeDefinition) Start(input run.Payload) (run.Execution, error) {
	decoded, err := input.Decode[crashTreeInput]()
	if err != nil {
		return nil, err
	}
	state := crashTreeState{Role: decoded.Role, Phase: crashTreePhaseReady}
	if !state.valid() {
		return nil, run.ErrInvalidPayload
	}
	return &crashTreeExecution{definition: c, state: state}, nil
}

func (c *crashTreeDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	decoded, err := state.Decode[crashTreeState](c.descriptor.Name())
	if err != nil {
		return nil, err
	}
	if !decoded.valid() {
		return nil, run.ErrInvalidExecutionState
	}
	return &crashTreeExecution{definition: c, state: decoded}, nil
}

type crashTreeExecution struct {
	definition *crashTreeDefinition
	state      crashTreeState
}

func (c *crashTreeExecution) Step(
	_ context.Context,
	signals []run.Signal,
) (run.Transition, error) {
	if c.state.Role == crashTreeRoleRoot {
		return c.stepRoot(signals)
	}
	return c.stepChild(signals)
}

func (c *crashTreeExecution) stepRoot(signals []run.Signal) (run.Transition, error) {
	switch c.state.phase() {
	case crashTreePhaseReady:
		return c.startRootChild(signals)
	case crashTreePhaseChildStarting:
		return c.openRootChildWait(signals)
	case crashTreePhaseRootWaitOpening:
		return c.enterRootChildWait(signals)
	case crashTreePhaseRootWaiting:
		return c.complete(signals, "runtest: child completion is missing", func(signal run.Signal) error {
			_, err := signal.Frame[run.ChildWaitSatisfied]()
			return err
		})
	default:
		return run.Transition{}, errors.New("runtest: root cannot advance from its current phase")
	}
}

func (c *crashTreeExecution) startRootChild(signals []run.Signal) (run.Transition, error) {
	if len(signals) != 0 {
		return run.Transition{}, errors.New("runtest: root received an unexpected initial Signal")
	}
	input, err := c.definition.descriptor.EncodeInput(crashTreeInput{Role: crashTreeRoleChild})
	if err != nil {
		return run.Transition{}, err
	}
	key, err := run.ParseChildKey(crashTreeChildKey)
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewChildStartEffect(run.ChildSpec{
		Key: key, ProgramRef: c.definition.reference, Input: input,
		Budget: crashTreeChildBudget(), Capabilities: run.CapabilitySet{},
	})
	if err != nil {
		return run.Transition{}, err
	}
	c.state.Phase = crashTreePhaseChildStarting
	return run.Continue(0, effect)
}

func (c *crashTreeExecution) openRootChildWait(signals []run.Signal) (run.Transition, error) {
	if len(signals) != 1 {
		return run.Transition{}, errors.New("runtest: child start result is missing")
	}
	started, err := signals[0].Frame[run.ChildStartResult]()
	if err != nil {
		return run.Transition{}, err
	}
	childID, ok := started.ProcessID()
	if !ok {
		return run.Transition{}, errors.New("runtest: child did not start")
	}
	key, err := run.ParseWaitKey(crashTreeRootWaitKey)
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewChildWaitEffect(run.ChildWaitSpec{
		Boundary: run.ChildWaitBoundaryResult,
		Key:      key, Children: []run.ProcessID{childID}, Condition: run.AllChildren(),
	})
	if err != nil {
		return run.Transition{}, err
	}
	c.state.ChildID = childID.String()
	return run.Continue(1, effect)
}

func (c *crashTreeExecution) enterRootChildWait(signals []run.Signal) (run.Transition, error) {
	if len(signals) != 1 {
		return run.Transition{}, errors.New("runtest: child wait acknowledgement is missing")
	}
	opened, err := signals[0].Frame[run.ChildWaitOpened]()
	if err != nil {
		return run.Transition{}, err
	}
	c.state.WaitID = opened.WaitID().String()
	return run.Wait(1, opened.WaitID())
}

func (c *crashTreeExecution) stepChild(signals []run.Signal) (run.Transition, error) {
	switch c.state.phase() {
	case crashTreePhaseReady:
		return c.openChildWait(signals)
	case crashTreePhaseChildWaitOpening:
		if len(signals) != 1 {
			return run.Transition{}, errors.New("runtest: external wait acknowledgement is missing")
		}
		waitID, addressed := signals[0].WaitID()
		if !addressed {
			return run.Transition{}, errors.New("runtest: external wait acknowledgement has no WaitID")
		}
		c.state.WaitID = waitID.String()
		return run.Wait(1, waitID)
	case crashTreePhaseChildWaiting:
		return c.complete(signals, "runtest: external wait response is missing", nil)
	default:
		return run.Transition{}, errors.New("runtest: child cannot advance from its current phase")
	}
}

func (c *crashTreeExecution) openChildWait(signals []run.Signal) (run.Transition, error) {
	if len(signals) != 0 {
		return run.Transition{}, errors.New("runtest: child received an unexpected initial Signal")
	}
	key, err := run.ParseWaitKey(crashTreeChildWaitKey)
	if err != nil {
		return run.Transition{}, err
	}
	spec, err := run.NewExternalWaitSpec(run.ExternalWaitSpecConfig{Key: key})
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewWaitEffect(spec)
	if err != nil {
		return run.Transition{}, err
	}
	c.state.Phase = crashTreePhaseChildWaitOpening
	return run.Continue(0, effect)
}

func (c *crashTreeExecution) complete(
	signals []run.Signal,
	missing string,
	validate func(run.Signal) error,
) (run.Transition, error) {
	if len(signals) != 1 {
		return run.Transition{}, errors.New(missing)
	}
	if validate != nil {
		if err := validate(signals[0]); err != nil {
			return run.Transition{}, err
		}
	}
	c.state.Phase = crashTreePhaseFinished
	output, err := run.EncodePayload(crashTreeOutput{Completed: true})
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(1, output)
}

func (c *crashTreeExecution) Snapshot() (run.ExecutionState, error) {
	payload, err := jsonv2.Marshal(c.state)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState(c.definition.descriptor.Name(), payload)
}

func newCrashTreeDeployment(t *testing.T) run.Program[run.Payload, run.Payload] {
	t.Helper()
	inputSchema, err := jsonschema.For[crashTreeInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := jsonschema.For[crashTreeOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: crashTreeDeploymentName, Description: crashTreeDeploymentDescription,
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &crashTreeDefinition{descriptor: descriptor}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition:           definition,
		ImplementationDigest: run.ComputeDigest([]byte(crashTreeImplementationSeed)),
		ConfigurationDigest:  run.ComputeDigest([]byte(crashTreeConfigurationSeed)),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.reference = deployment.ProgramRef()
	return deployment
}

func startCrashTree(
	t *testing.T,
	engine *run.Runtime,
	deployment run.Program[run.Payload, run.Payload],
) *run.Handle[run.Payload] {
	t.Helper()
	input, err := deployment.Descriptor().EncodeInput(crashTreeInput{Role: crashTreeRoleRoot})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func crashTreeChildID(
	t *testing.T,
	snapshot run.TreeSnapshot,
	rootID run.ProcessID,
) run.ProcessID {
	t.Helper()
	for _, process := range snapshot.ProcessSnapshots() {
		if process.Relation().ProcessID() != rootID &&
			process.Relation().Depth() == crashTreeDirectChildDepth {
			return process.Relation().ProcessID()
		}
	}
	t.Fatal("parked tree has no direct child")
	return run.ProcessID{}
}

var _ run.Definition = (*crashTreeDefinition)(nil)
