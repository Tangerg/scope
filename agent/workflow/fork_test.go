package workflow

import (
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent/run"

	"github.com/Tangerg/scope/core/jsonschema"
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
	branches := make([]ForkBranch, 0, 3)
	for _, id := range []string{"first", "second", "third"} {
		program := newManagedBranchProgram(t, id, tracker)
		branches = append(branches, ForkBranch{
			ID: id, Child: run.ChildBinding{Program: program, Budget: mustBudget(t)},
		})
	}
	stage, err := Fork(ForkConfig[forkInput, branchOutput, forkOutput]{
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
	if !stage.Valid() {
		t.Fatalf("Fork Stage = %#v", stage)
	}
	program := mustProgram(t, mustDefinition(t, "test.fork", stage), "fork")
	runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := run.EncodePayload(forkInput{Value: 7})
	resultChannel := make(chan run.Result[run.Payload], 1)
	errorChannel := make(chan error, 1)
	go func() {
		result, runErr := runtest.Observe(context.Background(), runtime, program, input)
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

	var result run.Result[run.Payload]
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
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestForkPropagatesLowestFailingBranch(t *testing.T) {
	branches := make([]ForkBranch, 0, 2)
	for _, id := range []string{"first", "second"} {
		branchID := id
		child := mustProgram(t, mustDefinition(t, "test.fork_failure_"+id,
			mustTransform(t, "fail", func(context.Context, forkInput) (branchOutput, error) {
				return branchOutput{}, fmt.Errorf("%s failed", branchID)
			}),
		), "fork-failure-"+id)
		branches = append(branches, ForkBranch{ID: id, Child: run.ChildBinding{Program: child, Budget: mustBudget(t)}})
	}
	stage, err := Fork(ForkConfig[forkInput, branchOutput, forkOutput]{
		ID: "workers", Branches: branches, WindowSize: 2,
		Reduce: func(context.Context, forkInput, []branchOutput) (forkOutput, error) { return forkOutput{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	program := mustProgram(t, mustDefinition(t, "test.fork_failure", stage), "fork-failure")
	runtime, _ := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	input, _ := run.EncodePayload(forkInput{Value: 1})
	result, err := runtest.Observe(context.Background(), runtime, program, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Termination().Status() != run.StatusFailed || !present || failure.Kind() != run.FailureKindExecution ||
		failure.Code() != "execution.step.failed" || failure.Message() != `transform "fail": first failed` {
		t.Fatalf("Fork failure = %#v", failure)
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestForkPreservesFailedAdmissions(t *testing.T) {
	for _, test := range []struct {
		name          string
		admitSibling  bool
		wantProcesses int
	}{
		{name: "all rejected", wantProcesses: 1},
		{name: "one admitted", admitSibling: true, wantProcesses: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			branches := make([]ForkBranch, 0, 2)
			for _, id := range []string{"refused", "available"} {
				child := mustProgram(t, mustDefinition(t, "test.admission_"+id,
					mustTransform(t, "identity", func(_ context.Context, input forkInput) (numberOutput, error) {
						return numberOutput(input), nil
					}),
				), "admission-"+id)
				branches = append(branches, ForkBranch{ID: id, Child: run.ChildBinding{Program: child, Budget: mustBudget(t)}})
			}
			stage, err := Fork(ForkConfig[forkInput, numberOutput, numberOutput]{
				ID: "workers", Branches: branches, WindowSize: 2,
				Reduce: func(context.Context, forkInput, []numberOutput) (numberOutput, error) {
					t.Error("reducer ran after a failed branch admission")
					return numberOutput{}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			store := run.NewMemoryStore()
			runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build,
				Store: store,
				ProcessAdmitter: run.ProcessAdmitterFunc(func(_ context.Context, admission run.ProcessAdmission) error {
					name := admission.ProgramRef().Name()
					if name == "test.admission_refused" || name == "test.admission_available" && !test.admitSibling {
						return errors.New("branch refused")
					}
					return nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := runtime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			input, err := run.EncodePayload(forkInput{Value: 1})
			if err != nil {
				t.Fatal(err)
			}
			root := mustProgram(t, mustDefinition(t, "test.admission_failure", stage), "admission-failure")
			result, err := runtest.Observe(t.Context(), runtime, root, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if result.Termination().Status() != run.StatusFailed || !failed || failure.Kind() != run.FailureKindExternal || failure.Code() != "runtime.child.admission.rejected" ||
				failure.Message() != "run: process admission rejected: branch refused" {
				t.Fatalf("failed admission lost its cause: %#v", failure)
			}
			log, _, err := store.Load(t.Context(), result.ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			for _, entry := range log.Entries {
				switch record := entry.Record.(type) {
				case run.Started:
					starts++
				case run.Exited:
					if record.Process != result.ProcessID() && record.Termination.Status() != run.StatusCompleted {
						t.Fatal("sibling failed to drain before parent")
					}
				}
			}
			if starts != test.wantProcesses {
				t.Fatalf("started %d, want %d", starts, test.wantProcesses)
			}
		})
	}
}

func TestForkRequiresExplicitValidWindowAndContracts(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "test.fork_valid",
		mustTransform(t, "valid", func(_ context.Context, input forkInput) (branchOutput, error) {
			return branchOutput{Branch: "valid", Value: input.Value}, nil
		}),
	), "fork-valid")
	validBranch := ForkBranch{ID: "valid", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)}}
	for name, config := range map[string]ForkConfig[forkInput, branchOutput, forkOutput]{
		"empty": {ID: "workers", WindowSize: 1, Reduce: func(context.Context, forkInput, []branchOutput) (forkOutput, error) { return forkOutput{}, nil }},
		"zero window size": {
			ID: "workers", Branches: []ForkBranch{validBranch},
			Reduce: func(context.Context, forkInput, []branchOutput) (forkOutput, error) { return forkOutput{}, nil },
		},
		"oversized window": {
			ID: "workers", Branches: []ForkBranch{validBranch}, WindowSize: 2,
			Reduce: func(context.Context, forkInput, []branchOutput) (forkOutput, error) { return forkOutput{}, nil },
		},
		"nil reducer": {ID: "workers", Branches: []ForkBranch{validBranch}, WindowSize: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Fork(config); !errors.Is(err, ErrInvalidStage) {
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
	descriptor run.Descriptor
	branch     string
}

func newManagedBranchProgram(t *testing.T, branch string, tracker *branchTracker) run.Program[run.Payload, run.Payload] {
	t.Helper()
	inputSchema, _ := jsonschema.For[forkInput]()
	outputSchema, _ := jsonschema.For[branchOutput]()
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name:        "test.branch_" + branch,
		Description: "Execute one controlled managed Fork branch.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &managedBranchDefinition{descriptor: descriptor, branch: branch}
	program, err := run.Bind[run.Payload, run.Payload](conformancetest.Compiler(t), run.BindingConfig{
		Definition: definition, Dispatcher: managedBranchDispatcher{tracker: tracker},
		Revision:      "managed-branch-implementation",
		Configuration: "managed-branch-" + branch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func (m *managedBranchDefinition) Descriptor() run.Descriptor {
	return m.descriptor
}

func (*managedBranchDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (m *managedBranchDefinition) Start(input run.Payload) (run.Execution, error) {
	decoded, err := input.Decode[forkInput]()
	if err != nil {
		return nil, err
	}
	return &managedBranchExecution{Branch: m.branch, Value: decoded.Value}, nil
}

func (m *managedBranchDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if state.Kind() != "test.branch" {
		return nil, run.ErrInvalidExecutionState
	}
	var execution managedBranchExecution
	if err := jsonv2.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	if execution.Branch != m.branch || execution.Phase > 2 {
		return nil, run.ErrInvalidExecutionState
	}
	return &execution, nil
}

type managedBranchExecution struct {
	Branch string `json:"branch"`
	Value  int    `json:"value"`
	Phase  uint8  `json:"phase"`
}

func (m *managedBranchExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	switch m.Phase {
	case 0:
		payload, _ := jsonv2.Marshal(struct {
			Branch string `json:"branch"`
			Value  int    `json:"value"`
		}{Branch: m.Branch, Value: m.Value})
		effect, err := run.NewDispatcherEffect(payload)
		if err != nil {
			return run.Transition{}, err
		}
		m.Phase = 1
		return run.Yield(0, []run.Effect{effect})
	case 1:
		if len(turn.Messages) != 1 {
			return run.Transition{}, errors.New("managed branch expected one settlement Signal")
		}
		output := turn.Messages[0].(run.Completion).Outcome.Payload()
		m.Phase = 2
		return run.Complete(1, output)
	default:
		return run.Transition{}, errors.New("managed branch already completed")
	}
}

func (m *managedBranchExecution) Snapshot() (run.ExecutionState, error) {
	payload, err := jsonv2.Marshal(m)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState("test.branch", payload)
}

type managedBranchDispatcher struct{ tracker *branchTracker }

func (m managedBranchDispatcher) Dispatch(
	ctx context.Context,
	request run.EffectRequest,
	_ run.DeltaEmitter,
) (run.Settlement, error) {
	var call struct {
		Branch string `json:"branch"`
		Value  int    `json:"value"`
	}
	if err := jsonv2.Unmarshal(request.Effect().Payload().JSON(), &call); err != nil {
		return run.Settlement{}, err
	}
	m.tracker.started <- call.Branch
	select {
	case <-m.tracker.releases[call.Branch]:
	case <-ctx.Done():
		return run.Settlement{}, ctx.Err()
	}
	payload, _ := jsonv2.Marshal(branchOutput{Branch: call.Branch, Value: call.Value})
	return run.NewSettlement(run.SettlementStatusSucceeded, payload)
}

func (managedBranchDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}
