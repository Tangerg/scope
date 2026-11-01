package workflow_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	startrand "crypto/rand"

	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestWaitingForkTreeRestoresWithoutDuplicateChildren(t *testing.T) {
	fixture := newRestorableForkFixture(t)
	snapshot, initialChildren := captureWaitingForkTree(t, fixture)
	completeRestoredForkTree(t, fixture, snapshot, initialChildren)
}

type restorableForkFixture struct {
	store *run.MemoryStore
	root  run.Program[run.Payload, run.Payload]
}

func newRestorableForkFixture(t *testing.T) restorableForkFixture {
	t.Helper()
	branches := make([]workflow.ForkBranch, 0, 3)
	for _, id := range []string{"first", "second", "third"} {
		deployment := newPausingBranchDeployment(t, id)
		branches = append(branches, workflow.ForkBranch{
			ID: id, Deployment: deployment, Budget: mustBudget(t),
		})
	}
	stage, err := workflow.Fork(workflow.ForkConfig[forkInput, branchOutput, forkOutput]{
		ID: "workers", Branches: branches, WindowSize: 2,
		Reduce: func(_ context.Context, _ forkInput, values []branchOutput) (forkOutput, error) {
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
	return restorableForkFixture{store: run.NewMemoryStore(), root: rootDeployment}
}

func captureWaitingForkTree(
	t *testing.T,
	fixture restorableForkFixture,
) (run.TreeSnapshot, []run.ProcessID) {
	t.Helper()
	engine, _ := run.New(t.Context(), run.Config{Store: fixture.store})
	input, _ := run.EncodePayload(forkInput{Value: 7})
	root, err := engine.Start(context.Background(), fixture.root, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := awaitPausedWindow(t, engine, root.Relation().ProcessID(), 3)
	initialChildren := childSnapshotIDs(snapshot)
	if len(initialChildren) != 2 {
		t.Fatalf("initial child count = %d", len(initialChildren))
	}
	t.Cleanup(func() {
		_ = root.Kill(context.Background(), "release retired writer")
		if err := root.Join(context.Background()); err != nil && !errors.Is(err, run.ErrTreeIncarnationConflict) {
			t.Error(err)
		}
		if err := engine.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return snapshot, initialChildren
}

func completeRestoredForkTree(
	t *testing.T,
	fixture restorableForkFixture,
	snapshot run.TreeSnapshot,
	initialChildren []run.ProcessID,
) {
	t.Helper()
	restoredEngine, _ := run.New(t.Context(), run.Config{Store: fixture.store})
	restoredRoot, err := restoredEngine.Recover(context.Background(), fixture.root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	resumePausedChildren(t, restoredEngine, initialChildren)
	secondWindow := awaitPausedWindow(t, restoredEngine, restoredRoot.Relation().ProcessID(), 4)
	thirdChild := newPausedChildID(secondWindow, initialChildren)
	if !thirdChild.Valid() {
		t.Fatal("restored Workflow did not create exactly one second-window child")
	}
	resumeProcess(t, restoredEngine, thirdChild)
	assertRestoredForkResult(t, restoredEngine, restoredRoot)
}

func resumePausedChildren(
	t *testing.T,
	engine *run.Runtime,
	children []run.ProcessID,
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
		if !present || report.Snapshot.Status() != run.StatusPaused {
			t.Fatalf("restored child %s status = %s", childID, report.Snapshot.Status())
		}
		if resumeErr := child.Resume(context.Background()); resumeErr != nil {
			t.Fatal(resumeErr)
		}
	}
}

func newPausedChildID(tree run.TreeSnapshot, existing []run.ProcessID) run.ProcessID {
	for _, process := range tree.ProcessSnapshots() {
		if process.Relation().Depth() == 1 && !slices.Contains(existing, process.Relation().ProcessID()) &&
			process.Status() == run.StatusPaused {
			return process.Relation().ProcessID()
		}
	}
	var none run.ProcessID
	return none
}

func resumeProcess(t *testing.T, engine *run.Runtime, processID run.ProcessID) {
	t.Helper()
	child, found := engine.Process(processID)
	if !found {
		t.Fatalf("second-window child %s was not registered", processID)
	}
	if resumeErr := child.Resume(context.Background()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
}

func assertRestoredForkResult(t *testing.T, engine *run.Runtime, root *run.Handle[run.Payload]) {
	t.Helper()
	result, err := root.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	output := decodeCompleted[forkOutput](t, result)
	if strings.Join(output.Branches, ",") != "first,second,third" || output.Total != 21 {
		t.Fatalf("restored Fork output = %#v", output)
	}
	finalTree, err := engine.CaptureTree(context.Background(), root.Relation().ProcessID())
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
	engine, _ := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	input, _ := run.EncodePayload(forkInput{Value: 1})
	root, err := engine.Start(context.Background(), rootDeployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	_ = awaitPausedWindow(t, engine, root.Relation().ProcessID(), 2)
	if requestCancellationErr := root.Cancel(context.Background(), "cancel Workflow consumer request"); requestCancellationErr != nil {
		t.Fatal(requestCancellationErr)
	}
	result, err := root.Await(context.Background())
	if err != nil || result.Termination().Status() != run.StatusCanceled ||
		result.Termination().Cause() != run.TerminationCauseHostCancellation {
		t.Fatalf("root cancellation = %#v, %v", result.Termination(), err)
	}
	tree, err := engine.CaptureTree(context.Background(), root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range tree.ProcessSnapshots() {
		if process.Relation().Depth() == 1 {
			child, found := engine.Process(process.Relation().ProcessID())
			if !found {
				t.Fatalf("child %s was not registered", process.Relation().ProcessID())
			}
			childResult, err := child.Await(context.Background())
			if err != nil || childResult.Termination().Status() != run.StatusCanceled ||
				childResult.Termination().Cause() != run.TerminationCauseParentCancellation {
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
	capability, _ := run.ParseCapability("test.guarded")
	capabilities, _ := run.NewCapabilitySet(capability)
	largeBudget := run.Budget{Steps: run.NewQuota(64), Effects: run.NewQuota(64), Signals: run.NewQuota(64)}
	smallBudget := run.Budget{Steps: run.NewQuota(2), Effects: run.NewQuota(2), Signals: run.NewQuota(2)}
	for _, test := range []struct {
		name         string
		budget       run.Budget
		capabilities run.CapabilitySet
		engine       run.Config
		wantCause    string
		wantKind     run.FailureKind
	}{
		{
			name: "budget", budget: largeBudget,
			engine: run.Config{
				Store:      run.NewMemoryStore(),
				Budget:     run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(16), Signals: run.NewQuota(16)},
				TreeLimits: run.TreeLimits{MaxPendingSignals: 16},
			},
			wantCause: "engine.child.budget_exhausted",
			wantKind:  run.FailureKindExecution,
		},
		{
			engine: run.Config{Store: run.NewMemoryStore()},
			name:   "capability", budget: smallBudget, capabilities: capabilities,
			wantCause: "engine.child.capability_escalation",
			wantKind:  run.FailureKindContract,
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
			engine, err := run.New(t.Context(), test.engine)
			if err != nil {
				t.Fatal(err)
			}
			input, _ := run.EncodePayload(numberInput{Value: 1})
			result, err := runtest.Observe(context.Background(), engine, root, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, present := result.Termination().Failure()
			if result.Termination().Status() != run.StatusFailed || !present || failure.Kind() != test.wantKind || failure.Code() != test.wantCause {
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
	engine *run.Runtime,
	rootID run.ProcessID,
	wantProcesses int,
) run.TreeSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		snapshot, err := engine.CaptureTree(ctx, rootID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.ProcessSnapshots()) == wantProcesses {
			rootWaiting := false
			allChildrenReady := true
			paused := 0
			for _, process := range snapshot.ProcessSnapshots() {
				if process.Relation().IsRoot() {
					rootWaiting = process.Status() == run.StatusWaiting
				} else if process.Status() == run.StatusPaused {
					paused++
				} else if process.Status() != run.StatusCompleted {
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

func childSnapshotIDs(snapshot run.TreeSnapshot) []run.ProcessID {
	var children []run.ProcessID
	for _, process := range snapshot.ProcessSnapshots() {
		if process.Relation().Depth() == 1 {
			children = append(children, process.Relation().ProcessID())
		}
	}
	return children
}

type pausingBranchDefinition struct {
	descriptor run.Descriptor
	branch     string
}

func newPausingBranchDeployment(t *testing.T, branch string) run.Program[run.Payload, run.Payload] {
	t.Helper()
	inputSchema, _ := jsonschema.For[forkInput]()
	outputSchema, _ := jsonschema.For[branchOutput]()
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name:        "test.workflow.pausing_" + branch,
		Description: "Pause one managed branch before producing its result.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &pausingBranchDefinition{descriptor: descriptor, branch: branch}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition:           definition,
		ImplementationDigest: run.ComputeDigest([]byte("pausing-branch-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte("pausing-branch-" + branch)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (p *pausingBranchDefinition) Descriptor() run.Descriptor {
	return p.descriptor
}

func (*pausingBranchDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (p *pausingBranchDefinition) Start(input run.Payload) (run.Execution, error) {
	decoded, err := input.Decode[forkInput]()
	if err != nil {
		return nil, err
	}
	return &pausingBranchExecution{Branch: p.branch, Value: decoded.Value}, nil
}

func (p *pausingBranchDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if state.Kind() != "test.workflow.pausing_branch" {
		return nil, run.ErrInvalidExecutionState
	}
	var execution pausingBranchExecution
	if err := jsonv2.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	if execution.Branch != p.branch || execution.Phase > 2 {
		return nil, run.ErrInvalidExecutionState
	}
	return &execution, nil
}

type pausingBranchExecution struct {
	Branch string `json:"branch"`
	Value  int    `json:"value"`
	Phase  uint8  `json:"phase"`
}

func (p *pausingBranchExecution) Step(_ context.Context, signals []run.Signal) (run.Transition, error) {
	if len(signals) != 0 {
		return run.Transition{}, errors.New("pausing branch received an unexpected Signal")
	}
	switch p.Phase {
	case 0:
		p.Phase = 1
		return run.Pause(0, "test branch is ready to resume")
	case 1:
		p.Phase = 2
		output, _ := run.EncodePayload(branchOutput{Branch: p.Branch, Value: p.Value})
		return run.Complete(0, output)
	default:
		return run.Transition{}, errors.New("pausing branch already completed")
	}
}

func (p *pausingBranchExecution) Snapshot() (run.ExecutionState, error) {
	payload, err := jsonv2.Marshal(p)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState("test.workflow.pausing_branch", payload)
}
