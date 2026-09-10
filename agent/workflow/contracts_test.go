package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestWaitingForkTreeRestoresWithoutDuplicateChildren(t *testing.T) {
	fixture := newRestorableForkFixture(t)
	snapshot, initialChildren := captureWaitingForkTree(t, fixture)
	completeRestoredForkTree(t, fixture, snapshot, initialChildren)
}

type restorableForkFixture struct {
	root     agent.Deployment
	resolver deploymentResolver
}

func newRestorableForkFixture(t *testing.T) restorableForkFixture {
	t.Helper()
	branches := make([]workflow.ForkBranch, 0, 3)
	resolver := deploymentResolver{}
	for _, id := range []string{"first", "second", "third"} {
		deployment := newPausingBranchDeployment(t, id)
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
	rootDeployment := mustDeployment(t, mustDefinition(t, "test.workflow.restorable_fork", stage), "restorable-fork")
	return restorableForkFixture{root: rootDeployment, resolver: resolver}
}

func captureWaitingForkTree(
	t *testing.T,
	fixture restorableForkFixture,
) (agent.TreeSnapshot, []agent.ProcessID) {
	t.Helper()
	engine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: fixture.resolver})
	input, _ := agent.EncodeInput(forkInput{Value: 7})
	root, err := engine.Start(context.Background(), fixture.root, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := awaitPausedWindow(t, engine, root.ID(), 3)
	initialChildren := childSnapshotIDs(snapshot)
	if len(initialChildren) != 2 {
		t.Fatalf("initial child count = %d", len(initialChildren))
	}
	if killErr := root.Kill(context.Background(), "replace captured Workflow tree"); killErr != nil {
		t.Fatal(killErr)
	}
	if result, awaitErr := root.Await(context.Background()); awaitErr != nil || result.Status() != agent.StatusKilled {
		t.Fatalf("original root result = %#v, %v", result, awaitErr)
	}
	if releaseErr := engine.ReleaseTree(context.Background(), root.ID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	return snapshot, initialChildren
}

func completeRestoredForkTree(
	t *testing.T,
	fixture restorableForkFixture,
	snapshot agent.TreeSnapshot,
	initialChildren []agent.ProcessID,
) {
	t.Helper()
	restoredEngine, _ := agent.NewEngine(agent.EngineConfig{DeploymentResolver: fixture.resolver})
	restoredRoot, err := restoredEngine.RestoreTree(context.Background(), fixture.root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	resumePausedChildren(t, restoredEngine, initialChildren)
	secondWindow := awaitPausedWindow(t, restoredEngine, restoredRoot.ID(), 4)
	thirdChild := newPausedChildID(secondWindow, initialChildren)
	if !thirdChild.Valid() {
		t.Fatal("restored Workflow did not create exactly one second-window child")
	}
	resumeProcess(t, restoredEngine, thirdChild)
	assertRestoredForkResult(t, restoredEngine, restoredRoot)
}

func resumePausedChildren(
	t *testing.T,
	engine *agent.Engine,
	children []agent.ProcessID,
) {
	t.Helper()
	for _, childID := range slices.Backward(children) {
		child, found := engine.Process(childID)
		if !found {
			t.Fatalf("restored child %s was not registered", childID)
		}
		inspection, err := engine.InspectTree(t.Context(), child.Relation().RootID())
		if err != nil {
			t.Fatal(err)
		}
		report, present := inspection.Process(childID)
		if !present || report.Snapshot.Status() != agent.StatusPaused {
			t.Fatalf("restored child %s status = %s", childID, report.Snapshot.Status())
		}
		if resumeErr := child.Resume(context.Background()); resumeErr != nil {
			t.Fatal(resumeErr)
		}
	}
}

func newPausedChildID(tree agent.TreeSnapshot, existing []agent.ProcessID) agent.ProcessID {
	for _, process := range tree.ProcessSnapshots() {
		if process.Relation().Depth() == 1 && !slices.Contains(existing, process.ProcessID()) &&
			process.Status() == agent.StatusPaused {
			return process.ProcessID()
		}
	}
	var none agent.ProcessID
	return none
}

func resumeProcess(t *testing.T, engine *agent.Engine, processID agent.ProcessID) {
	t.Helper()
	child, found := engine.Process(processID)
	if !found {
		t.Fatalf("second-window child %s was not registered", processID)
	}
	if resumeErr := child.Resume(context.Background()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
}

func assertRestoredForkResult(t *testing.T, engine *agent.Engine, root *agent.Process) {
	t.Helper()
	result, err := root.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	output := decodeCompleted[forkOutput](t, result)
	if strings.Join(output.Branches, ",") != "first,second,third" || output.Total != 21 {
		t.Fatalf("restored Fork output = %#v", output)
	}
	finalTree, err := engine.CaptureTree(context.Background(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(finalTree.ProcessSnapshots()) != 4 {
		t.Fatalf("restored final tree size = %d", len(finalTree.ProcessSnapshots()))
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowCancellationPropagatesToPausedChild(t *testing.T) {
	childDeployment := newPausingBranchDeployment(t, "only")
	call, err := workflow.Call(workflow.CallConfig{
		ID: "child", Deployment: childDeployment, Budget: mustBudget(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	rootDeployment := mustDeployment(t, mustDefinition(t, "test.workflow.cancel", call), "cancel")
	engine, _ := agent.NewEngine(agent.EngineConfig{
		DeploymentResolver: deploymentResolver{childDeployment.DeploymentRef(): childDeployment},
	})
	input, _ := agent.EncodeInput(forkInput{Value: 1})
	root, err := engine.Start(context.Background(), rootDeployment, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = awaitPausedWindow(t, engine, root.ID(), 2)
	if requestCancellationErr := root.RequestCancellation(context.Background(), "cancel Workflow consumer request"); requestCancellationErr != nil {
		t.Fatal(requestCancellationErr)
	}
	result, err := root.Await(context.Background())
	if err != nil || result.Status() != agent.StatusCanceled ||
		result.Termination().Cause() != agent.TerminationCauseHostCancellation {
		t.Fatalf("root cancellation = %#v, %v", result.Termination(), err)
	}
	tree, err := engine.CaptureTree(context.Background(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range tree.ProcessSnapshots() {
		if process.Relation().Depth() == 1 {
			child, found := engine.Process(process.ProcessID())
			if !found {
				t.Fatalf("child %s was not registered", process.ProcessID())
			}
			childResult, err := child.Await(context.Background())
			if err != nil || childResult.Status() != agent.StatusCanceled ||
				childResult.Termination().Cause() != agent.TerminationCauseParentCancellation {
				t.Fatalf("child cancellation = %#v, %v", childResult.Termination(), err)
			}
		}
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestCallCannotEscalateBudgetOrCapabilities(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.guarded_child",
		mustTransform(t, "identity", func(_ context.Context, input numberInput) (numberInput, error) { return input, nil }),
	), "guarded-child")
	capability, _ := agent.ParseCapability("test.guarded")
	capabilities, _ := agent.NewCapabilitySet(capability)
	largeBudget := agent.Budget{Steps: 64, Effects: 64, Signals: 64}
	smallBudget := agent.Budget{Steps: 2, Effects: 2, Signals: 2}
	for _, test := range []struct {
		name         string
		budget       agent.Budget
		capabilities agent.CapabilitySet
		engine       agent.EngineConfig
		wantCause    string
	}{
		{
			name: "budget", budget: largeBudget,
			engine: agent.EngineConfig{Limits: agent.Limits{
				MaxSteps: 16, MaxEffects: 16, MaxSignals: 16, MaxPendingSignals: 16,
			}},
			wantCause: "engine.child.budget_exhausted",
		},
		{
			name: "capability", budget: smallBudget, capabilities: capabilities,
			wantCause: "engine.child.capability_escalation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			call, err := workflow.Call(workflow.CallConfig{
				ID: "child", Deployment: child, Budget: test.budget, Capabilities: test.capabilities,
			})
			if err != nil {
				t.Fatal(err)
			}
			root := mustDeployment(t, mustDefinition(t, "test.workflow.guard_"+test.name, call), "guard-"+test.name)
			engine, err := agent.NewEngine(test.engine)
			if err != nil {
				t.Fatal(err)
			}
			input, _ := agent.EncodeInput(numberInput{Value: 1})
			result, err := engine.Run(context.Background(), root, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, present := result.Termination().Failure()
			if result.Status() != agent.StatusFailed || !present || failure.Code() != "workflow.call.start_failed" ||
				!strings.Contains(failure.Message(), test.wantCause) {
				t.Fatalf("guard failure = %#v", failure)
			}
			if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func awaitPausedWindow(
	t *testing.T,
	engine *agent.Engine,
	rootID agent.ProcessID,
	wantProcesses int,
) agent.TreeSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		snapshot, err := engine.CaptureTree(ctx, rootID)
		if err == nil && len(snapshot.ProcessSnapshots()) == wantProcesses {
			rootWaiting := false
			allChildrenReady := true
			paused := 0
			for _, process := range snapshot.ProcessSnapshots() {
				if process.Relation().IsRoot() {
					rootWaiting = process.Status() == agent.StatusWaiting
				} else if process.Status() == agent.StatusPaused {
					paused++
				} else if process.Status() != agent.StatusCompleted {
					allChildrenReady = false
				}
			}
			if rootWaiting && paused > 0 && allChildrenReady {
				return snapshot
			}
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("tree %s did not reach %d Processes with a paused window: %v", rootID, wantProcesses, err)
		}
		runtime.Gosched()
	}
}

func childSnapshotIDs(snapshot agent.TreeSnapshot) []agent.ProcessID {
	var children []agent.ProcessID
	for _, process := range snapshot.ProcessSnapshots() {
		if process.Relation().Depth() == 1 {
			children = append(children, process.ProcessID())
		}
	}
	return children
}

type pausingBranchDefinition struct {
	descriptor agent.Descriptor
	branch     string
}

func newPausingBranchDeployment(t *testing.T, branch string) agent.Deployment {
	t.Helper()
	inputSchema, _ := agent.SchemaFor[forkInput]()
	outputSchema, _ := agent.SchemaFor[branchOutput]()
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name:        "test.workflow.pausing_" + branch,
		Description: "Pause one managed branch before producing its result.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &pausingBranchDefinition{descriptor: descriptor, branch: branch}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte("pausing-branch-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("pausing-branch-" + branch)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (p *pausingBranchDefinition) Descriptor() agent.Descriptor {
	return p.descriptor
}

func (p *pausingBranchDefinition) Start(input agent.Input) (agent.Execution, error) {
	decoded, err := input.Decode[forkInput]()
	if err != nil {
		return nil, err
	}
	return &pausingBranchExecution{Branch: p.branch, Value: decoded.Value}, nil
}

func (p *pausingBranchDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != "test.workflow.pausing_branch" {
		return nil, agent.ErrInvalidExecutionState
	}
	var execution pausingBranchExecution
	if err := json.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	if execution.Branch != p.branch || execution.Phase > 2 {
		return nil, agent.ErrInvalidExecutionState
	}
	return &execution, nil
}

type pausingBranchExecution struct {
	Branch string `json:"branch"`
	Value  int    `json:"value"`
	Phase  uint8  `json:"phase"`
}

func (p *pausingBranchExecution) Step(_ context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 0 {
		return agent.Transition{}, errors.New("pausing branch received an unexpected Signal")
	}
	switch p.Phase {
	case 0:
		p.Phase = 1
		return agent.Pause(0, "test branch is ready to resume")
	case 1:
		p.Phase = 2
		output, _ := agent.EncodeOutput(branchOutput{Branch: p.Branch, Value: p.Value})
		return agent.Complete(0, output)
	default:
		return agent.Transition{}, errors.New("pausing branch already completed")
	}
}

func (p *pausingBranchExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("test.workflow.pausing_branch", payload)
}
