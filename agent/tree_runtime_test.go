package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

const treeRuntimeProgressTimeout = 5 * time.Second

type treeRuntimeContextKey struct{}

type treeRuntimeStepContext struct {
	hasDeadline bool
	hasValue    bool
	cause       error
}

func TestTreeRuntimeDoesNotLetSlowStepStarveSibling(t *testing.T) {
	deployment, probe := newTreeRuntimeTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	hostContext := context.WithValue(context.Background(), treeRuntimeContextKey{}, "host value")
	hostContext, cancelHost := context.WithTimeout(hostContext, time.Minute)
	defer cancelHost()
	input, _ := EncodeInput(treeRuntimeTestInput{Role: treeRuntimeRoleRoot})
	root, err := engine.Start(hostContext, deployment, input)
	if err != nil {
		t.Fatal(err)
	}

	observed := receiveTreeRuntimeProbe(t, probe.blockedStepStarted)
	if observed.hasDeadline || observed.hasValue || observed.cause != nil {
		t.Fatalf("Step context leaked host ambient state: %+v", observed)
	}
	receiveTreeRuntimeProbe(t, probe.fastStepStarted)

	blockedID := deriveChildProcessID(deriveEffectID(root.ID(), 1, 0))
	fastID := deriveChildProcessID(deriveEffectID(root.ID(), 1, 1))
	blocked, exists := engine.Process(blockedID)
	if !exists {
		t.Fatal("blocked child was not published")
	}
	fast, exists := engine.Process(fastID)
	if !exists {
		t.Fatal("fast child was not published")
	}
	awaitContext, cancelAwait := context.WithTimeout(context.Background(), treeRuntimeProgressTimeout)
	fastResult, err := fast.Await(awaitContext)
	cancelAwait()
	if err != nil {
		t.Fatalf("fast sibling was starved by blocked Step: %v", err)
	}
	if fastResult.Status() != StatusCompleted {
		t.Fatalf("fast sibling status = %s, want %s", fastResult.Status(), StatusCompleted)
	}

	if killErr := blocked.Kill(context.Background(), "cancel blocked Step"); killErr != nil {
		t.Fatal(killErr)
	}
	receiveTreeRuntimeProbe(t, probe.blockedStepCanceled)
	blockedResult, err := blocked.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if blockedResult.Status() != StatusKilled {
		t.Fatalf("blocked child status = %s, want %s", blockedResult.Status(), StatusKilled)
	}
	if err := root.Kill(context.Background(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Await(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func receiveTreeRuntimeProbe[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(treeRuntimeProgressTimeout):
		t.Fatal("tree runtime probe timed out")
		var zero T
		return zero
	}
}

const (
	treeRuntimeRoleRoot    = "root"
	treeRuntimeRoleBlocked = "blocked"
	treeRuntimeRoleFast    = "fast"
	treeRuntimeStateKind   = "test.tree_runtime"
)

type treeRuntimeTestInput struct {
	Role string `json:"role"`
}

type treeRuntimeTestOutput struct {
	Role string `json:"role"`
}

type treeRuntimeTestState struct {
	Role  string `json:"role"`
	Phase uint8  `json:"phase"`
}

type treeRuntimeTestProbe struct {
	blockedStepStarted  chan treeRuntimeStepContext
	blockedStepCanceled chan struct{}
	fastStepStarted     chan struct{}
	fastStepReady       chan struct{}
	fastStepRelease     chan struct{}
	blockedStartedOnce  sync.Once
	blockedCanceledOnce sync.Once
	fastReadyOnce       sync.Once
	fastStartedOnce     sync.Once
}

func newTreeRuntimeTestProbe() *treeRuntimeTestProbe {
	return &treeRuntimeTestProbe{
		blockedStepStarted:  make(chan treeRuntimeStepContext, 1),
		blockedStepCanceled: make(chan struct{}),
		fastStepStarted:     make(chan struct{}),
		fastStepReady:       make(chan struct{}),
	}
}

type treeRuntimeTestDefinition struct {
	descriptor Descriptor
	reference  DeploymentRef
	probe      *treeRuntimeTestProbe
}

func newTreeRuntimeTestDeployment(t testing.TB) (Deployment, *treeRuntimeTestProbe) {
	t.Helper()
	inputSchema, err := SchemaFor[treeRuntimeTestInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := SchemaFor[treeRuntimeTestOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "test.tree_runtime", Description: "Verify tree owner scheduling isolation.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	implementation := ComputeDigest([]byte("tree-runtime-test-implementation"))
	configuration := ComputeDigest([]byte("tree-runtime-test-configuration"))
	reference, err := newDeploymentRef(descriptor, implementation, configuration)
	if err != nil {
		t.Fatal(err)
	}
	probe := newTreeRuntimeTestProbe()
	definition := &treeRuntimeTestDefinition{
		descriptor: descriptor, reference: reference, probe: probe,
	}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: definition, Dispatcher: childTestDispatcher{},
		ImplementationDigest: implementation, ConfigurationDigest: configuration,
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment, probe
}

func (t *treeRuntimeTestDefinition) Descriptor() Descriptor { return t.descriptor }

func (t *treeRuntimeTestDefinition) Start(input Input) (Execution, error) {
	decoded, err := input.Decode[treeRuntimeTestInput]()
	if err != nil {
		return nil, err
	}
	return &treeRuntimeTestExecution{
		definition: t, state: treeRuntimeTestState{Role: decoded.Role},
	}, nil
}

func (t *treeRuntimeTestDefinition) Restore(state ExecutionState) (Execution, error) {
	if state.Kind() != treeRuntimeStateKind {
		return nil, ErrInvalidExecutionState
	}
	var decoded treeRuntimeTestState
	if err := json.Unmarshal(state.Payload(), &decoded); err != nil {
		return nil, err
	}
	return &treeRuntimeTestExecution{definition: t, state: decoded}, nil
}

type treeRuntimeTestExecution struct {
	definition *treeRuntimeTestDefinition
	state      treeRuntimeTestState
}

func (t *treeRuntimeTestExecution) Step(ctx context.Context, signals []Signal) (Transition, error) {
	switch t.state.Role {
	case treeRuntimeRoleRoot:
		return t.stepRoot(signals)
	case treeRuntimeRoleFast:
		if t.definition.probe.fastStepRelease != nil {
			t.definition.probe.fastReadyOnce.Do(func() {
				close(t.definition.probe.fastStepReady)
			})
			select {
			case <-t.definition.probe.fastStepRelease:
			case <-ctx.Done():
				return Transition{}, ctx.Err()
			}
		}
		t.definition.probe.fastStartedOnce.Do(func() {
			close(t.definition.probe.fastStepStarted)
		})
		return t.complete()
	case treeRuntimeRoleBlocked:
		return t.stepBlocked(ctx)
	default:
		return Transition{}, ErrInvalidExecutionState
	}
}

func (t *treeRuntimeTestExecution) stepRoot(signals []Signal) (Transition, error) {
	if t.state.Phase > 0 {
		return Pause(uint32(len(signals)), "keep root alive while children run")
	}
	t.state.Phase = 1
	roles := []string{treeRuntimeRoleBlocked, treeRuntimeRoleFast}
	effects := make([]Effect, 0, len(roles))
	for _, role := range roles {
		input, _ := EncodeInput(treeRuntimeTestInput{Role: role})
		key, _ := ParseChildKey(role)
		budget, _ := NewBudget(BudgetConfig{Steps: 4, Effects: 4, Signals: 4})
		effect, err := StartChild(ChildSpec{
			Key: key, DeploymentRef: t.definition.reference, Input: input, Budget: budget,
		})
		if err != nil {
			return Transition{}, err
		}
		effects = append(effects, effect)
	}
	return Continue(0, effects...)
}

func (t *treeRuntimeTestExecution) stepBlocked(ctx context.Context) (Transition, error) {
	_, hasDeadline := ctx.Deadline()
	observation := treeRuntimeStepContext{
		hasDeadline: hasDeadline,
		hasValue:    ctx.Value(treeRuntimeContextKey{}) != nil,
		cause:       context.Cause(ctx),
	}
	t.definition.probe.blockedStartedOnce.Do(func() {
		t.definition.probe.blockedStepStarted <- observation
	})
	<-ctx.Done()
	t.definition.probe.blockedCanceledOnce.Do(func() {
		close(t.definition.probe.blockedStepCanceled)
	})
	return Transition{}, ctx.Err()
}

func (t *treeRuntimeTestExecution) complete() (Transition, error) {
	t.state.Phase++
	output, _ := EncodeOutput(treeRuntimeTestOutput{Role: t.state.Role})
	return Complete(0, output)
}

func (t *treeRuntimeTestExecution) Snapshot() (ExecutionState, error) {
	payload, err := json.Marshal(t.state)
	if err != nil {
		return ExecutionState{}, err
	}
	return NewExecutionState(treeRuntimeStateKind, payload)
}
