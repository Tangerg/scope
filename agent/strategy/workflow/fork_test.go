package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

type forkInput struct {
	Value int `json:"value"`
}

type branchOutput struct {
	Branch string `json:"branch"`
	Value  int    `json:"value"`
}

type forkOutput struct {
	Branches []string `json:"branches"`
	Total    int      `json:"total"`
}

func TestForkUsesBoundedWindowsAndDeclarationOrder(t *testing.T) {
	synctest.Test(t, testForkUsesBoundedWindowsAndDeclarationOrder)
}

func testForkUsesBoundedWindowsAndDeclarationOrder(t *testing.T) {
	tracker := newBranchTracker("first", "second", "third")
	branches := make([]workflow.ForkBranch, 0, 3)
	resolver := deploymentResolver{}
	for _, id := range []string{"first", "second", "third"} {
		deployment := newManagedBranchDeployment(t, id, tracker)
		resolver[deployment.DeploymentRef()] = deployment
		branches = append(branches, workflow.ForkBranch{
			ID: id, Deployment: deployment, Budget: mustBudget(t),
		})
	}
	stage, err := workflow.Fork(workflow.ForkConfig[forkInput, branchOutput, forkOutput]{
		ID: "workers", Branches: branches, WindowSize: 2,
		Reduce: func(_ context.Context, values []branchOutput) (forkOutput, error) {
			result := forkOutput{Branches: make([]string, len(values))}
			for index, value := range values {
				result.Branches[index] = value.Branch
				result.Total += value.Value
			}
			return result, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stage.Valid() {
		t.Fatalf("Fork Stage = %#v", stage)
	}
	deployment := mustDeployment(t, mustDefinition(t, "test.workflow.fork", stage), "fork")
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := agent.EncodeInput(forkInput{Value: 7})
	resultChannel := make(chan agent.Result, 1)
	errorChannel := make(chan error, 1)
	go func() {
		result, runErr := engine.Run(context.Background(), deployment, input)
		if runErr != nil {
			errorChannel <- runErr
			return
		}
		resultChannel <- result
	}()

	started := map[string]bool{tracker.awaitStart(t): true, tracker.awaitStart(t): true}
	if !started["first"] || !started["second"] {
		t.Fatalf("first window started = %#v", started)
	}
	tracker.assertNotStarted(t)
	tracker.release("second")
	tracker.release("first")
	if got := tracker.awaitStart(t); got != "third" {
		t.Fatalf("second window started %q", got)
	}
	tracker.release("third")

	var result agent.Result
	select {
	case err := <-errorChannel:
		t.Fatal(err)
	case result = <-resultChannel:
	case <-time.After(5 * time.Second):
		t.Fatal("Fork did not complete")
	}
	output := decodeCompleted[forkOutput](t, result)
	if strings.Join(output.Branches, ",") != "first,second,third" || output.Total != 21 {
		t.Fatalf("Fork output = %#v", output)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestForkAttributesLowestFailingBranch(t *testing.T) {
	branches := make([]workflow.ForkBranch, 0, 2)
	resolver := deploymentResolver{}
	for _, id := range []string{"first", "second"} {
		branchID := id
		child := mustDeployment(t, mustDefinition(t, "test.workflow.fork_failure_"+id,
			mustTransform(t, "fail", func(context.Context, forkInput) (branchOutput, error) {
				return branchOutput{}, fmt.Errorf("%s failed", branchID)
			}),
		), "fork-failure-"+id)
		resolver[child.DeploymentRef()] = child
		branches = append(branches, workflow.ForkBranch{ID: id, Deployment: child, Budget: mustBudget(t)})
	}
	stage, err := workflow.Fork(workflow.ForkConfig[forkInput, branchOutput, forkOutput]{
		ID: "workers", Branches: branches, WindowSize: 2,
		Reduce: func(context.Context, []branchOutput) (forkOutput, error) { return forkOutput{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := mustDeployment(t, mustDefinition(t, "test.workflow.fork_failure", stage), "fork-failure")
	engine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
	input, _ := agent.EncodeInput(forkInput{Value: 1})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Status() != agent.StatusFailed || !present || failure.Code() != "workflow.fork.branch_failed" ||
		!strings.Contains(failure.Message(), "branch first") {
		t.Fatalf("Fork failure = %#v", failure)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestForkRequiresExplicitValidWindowAndContracts(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.fork_valid",
		mustTransform(t, "valid", func(_ context.Context, input forkInput) (branchOutput, error) {
			return branchOutput{Branch: "valid", Value: input.Value}, nil
		}),
	), "fork-valid")
	validBranch := workflow.ForkBranch{ID: "valid", Deployment: child, Budget: mustBudget(t)}
	for name, config := range map[string]workflow.ForkConfig[forkInput, branchOutput, forkOutput]{
		"empty": {ID: "workers", WindowSize: 1, Reduce: func(context.Context, []branchOutput) (forkOutput, error) { return forkOutput{}, nil }},
		"zero window size": {
			ID: "workers", Branches: []workflow.ForkBranch{validBranch},
			Reduce: func(context.Context, []branchOutput) (forkOutput, error) { return forkOutput{}, nil },
		},
		"oversized window": {
			ID: "workers", Branches: []workflow.ForkBranch{validBranch}, WindowSize: 2,
			Reduce: func(context.Context, []branchOutput) (forkOutput, error) { return forkOutput{}, nil },
		},
		"nil reducer": {ID: "workers", Branches: []workflow.ForkBranch{validBranch}, WindowSize: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workflow.Fork(config); !errors.Is(err, workflow.ErrInvalidStage) {
				t.Fatalf("Fork error = %v", err)
			}
		})
	}
}

type branchTracker struct {
	started  chan string
	releases map[string]chan struct{}
}

func newBranchTracker(ids ...string) *branchTracker {
	tracker := &branchTracker{started: make(chan string, len(ids)), releases: make(map[string]chan struct{}, len(ids))}
	for _, id := range ids {
		tracker.releases[id] = make(chan struct{})
	}
	return tracker
}

func (b *branchTracker) awaitStart(t *testing.T) string {
	t.Helper()
	select {
	case id := <-b.started:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("branch did not start")
		return ""
	}
}

func (b *branchTracker) assertNotStarted(t *testing.T) {
	t.Helper()
	synctest.Wait()
	select {
	case id := <-b.started:
		t.Fatalf("branch %q escaped the execution window", id)
	default:
	}
}

func (b *branchTracker) release(id string) { close(b.releases[id]) }

type managedBranchDefinition struct {
	descriptor agent.Descriptor
	branch     string
}

func newManagedBranchDeployment(t *testing.T, branch string, tracker *branchTracker) agent.Deployment {
	t.Helper()
	inputSchema, _ := agent.SchemaFor[forkInput]()
	outputSchema, _ := agent.SchemaFor[branchOutput]()
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name:        "test.workflow.branch_" + branch,
		Description: "Execute one controlled managed Fork branch.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &managedBranchDefinition{descriptor: descriptor, branch: branch}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: managedBranchDispatcher{tracker: tracker},
		ImplementationDigest: agent.ComputeDigest([]byte("managed-branch-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("managed-branch-" + branch)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (m *managedBranchDefinition) Descriptor() agent.Descriptor {
	return m.descriptor
}

func (m *managedBranchDefinition) Start(input agent.Input) (agent.Execution, error) {
	decoded, err := input.Decode[forkInput]()
	if err != nil {
		return nil, err
	}
	return &managedBranchExecution{Branch: m.branch, Value: decoded.Value}, nil
}

func (m *managedBranchDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != "test.workflow.branch" {
		return nil, agent.ErrInvalidExecutionState
	}
	var execution managedBranchExecution
	if err := json.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	if execution.Branch != m.branch || execution.Phase > 2 {
		return nil, agent.ErrInvalidExecutionState
	}
	return &execution, nil
}

type managedBranchExecution struct {
	Branch string `json:"branch"`
	Value  int    `json:"value"`
	Phase  uint8  `json:"phase"`
}

func (m *managedBranchExecution) Step(_ context.Context, signals []agent.Signal) (agent.Transition, error) {
	switch m.Phase {
	case 0:
		payload, _ := json.Marshal(struct {
			Branch string `json:"branch"`
			Value  int    `json:"value"`
		}{Branch: m.Branch, Value: m.Value})
		effect, err := agent.NewDispatcherEffect(payload)
		if err != nil {
			return agent.Transition{}, err
		}
		m.Phase = 1
		return agent.Continue(0, effect)
	case 1:
		if len(signals) != 1 {
			return agent.Transition{}, errors.New("managed branch expected one settlement Signal")
		}
		output, err := agent.ParseOutput(signals[0].Payload())
		if err != nil {
			return agent.Transition{}, err
		}
		m.Phase = 2
		return agent.Complete(1, output)
	default:
		return agent.Transition{}, errors.New("managed branch already completed")
	}
}

func (m *managedBranchExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("test.workflow.branch", payload)
}

type managedBranchDispatcher struct{ tracker *branchTracker }

func (m managedBranchDispatcher) Dispatch(
	ctx context.Context,
	request agent.EffectRequest,
	_ agent.DeltaEmitter,
) (agent.Settlement, error) {
	var call struct {
		Branch string `json:"branch"`
		Value  int    `json:"value"`
	}
	if err := json.Unmarshal(request.Effect().Payload(), &call); err != nil {
		return agent.Settlement{}, err
	}
	m.tracker.started <- call.Branch
	select {
	case <-m.tracker.releases[call.Branch]:
	case <-ctx.Done():
		return agent.Settlement{}, ctx.Err()
	}
	payload, _ := json.Marshal(branchOutput{Branch: call.Branch, Value: call.Value})
	return agent.NewSettlement(request.ID(), agent.SettlementStatusSucceeded, payload)
}

func (managedBranchDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}
