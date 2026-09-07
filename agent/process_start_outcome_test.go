package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestProcessStartOutcomesConcludeAcceptedRootAndChildAdmissions(t *testing.T) {
	for _, mode := range []struct {
		name       string
		durability TreeDurability
	}{
		{name: "ephemeral"},
		{name: "durable", durability: &recordingTreeDurability{}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			childDeployment := newChildTestDeployment(t)
			parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
			var engine *Engine
			var mu sync.Mutex
			var outcomes []ProcessStartOutcome
			acknowledger := ProcessStartOutcomeAcknowledgerFunc(func(
				_ context.Context,
				outcome ProcessStartOutcome,
			) error {
				if !outcome.Valid() {
					t.Fatal("acknowledger received an invalid outcome")
				}
				if _, published := engine.Process(outcome.Admission().Relation().ProcessID()); published {
					t.Fatal("started outcome was acknowledged after Process publication")
				}
				mu.Lock()
				outcomes = append(outcomes, outcome)
				mu.Unlock()
				return nil
			})
			var err error
			engine, err = NewEngine(EngineConfig{
				TreeDurability:                  mode.durability,
				DeploymentResolver:              deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment},
				ProcessStartOutcomeAcknowledger: acknowledger,
			})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := EncodeInput(struct{}{})
			parent, err := engine.Start(t.Context(), parentDeployment, input)
			if err != nil {
				t.Fatal(err)
			}
			parentOutput := childTestResult(t, mustAwait(t, parent))
			if len(parentOutput.ChildIDs) != 1 || parentOutput.Failures != 0 {
				t.Fatalf("parent output = %#v", parentOutput)
			}
			childID, _ := ParseProcessID(parentOutput.ChildIDs[0])
			child, found := engine.Process(childID)
			if !found {
				t.Fatal("started child is missing")
			}
			_ = mustAwait(t, child)

			mu.Lock()
			got := append([]ProcessStartOutcome(nil), outcomes...)
			mu.Unlock()
			if len(got) != 2 {
				t.Fatalf("outcomes = %d, want root and child", len(got))
			}
			byProcess := make(map[ProcessID]ProcessStartOutcome, len(got))
			for _, outcome := range got {
				if outcome.Status() != ProcessStartOutcomeStatusStarted {
					t.Fatalf("outcome status = %s, want started", outcome.Status())
				}
				if _, failed := outcome.Failure(); failed {
					t.Fatal("started outcome exposes a Failure")
				}
				byProcess[outcome.Admission().Relation().ProcessID()] = outcome
			}
			rootOutcome, rootFound := byProcess[parent.ID()]
			childOutcome, childFound := byProcess[child.ID()]
			rootStartedAt, rootStarted := rootOutcome.StartedAt()
			if !rootFound || !rootOutcome.Admission().Relation().IsRoot() ||
				!rootStarted || rootStartedAt != parent.StartedAt() {
				t.Fatalf("root outcome = %#v", rootOutcome)
			}
			parentID, hasParent := childOutcome.Admission().Relation().ParentID()
			childStartedAt, childStarted := childOutcome.StartedAt()
			if !childFound || !hasParent || parentID != parent.ID() ||
				!childStarted || childStartedAt != child.StartedAt() {
				t.Fatalf("child outcome = %#v", childOutcome)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProcessStartOutcomeReportsPostAdmissionInitializationFailure(t *testing.T) {
	tests := []struct {
		name  string
		stage initializationFailureStage
		code  string
	}{
		{name: "Definition start", stage: failDefinitionStart, code: "engine.process.start.failed"},
		{name: "initial snapshot", stage: failInitialSnapshot, code: "engine.process.snapshot.failed"},
		{name: "initial restore", stage: failInitialRestore, code: "engine.process.snapshot.unrestorable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			initializationErr := errors.New("injected initialization failure")
			deployment := failingInitializationDeployment(t, test.stage, initializationErr)
			var outcomes []ProcessStartOutcome
			engine, err := NewEngine(EngineConfig{
				ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(
					_ context.Context,
					outcome ProcessStartOutcome,
				) error {
					outcomes = append(outcomes, outcome)
					return nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := EncodeInput(childTestInput{Mode: "leaf"})
			process, err := engine.Start(t.Context(), deployment, input)
			if process != nil || !errors.Is(err, initializationErr) {
				t.Fatalf("Start process=%v error=%v", process, err)
			}
			if len(outcomes) != 1 || outcomes[0].Status() != ProcessStartOutcomeStatusAborted {
				t.Fatalf("outcomes = %#v", outcomes)
			}
			failure, failed := outcomes[0].Failure()
			if !failed || failure.Code() != test.code {
				t.Fatalf("aborted failure = %#v, present = %t", failure, failed)
			}
			if startedAt, started := outcomes[0].StartedAt(); started || !startedAt.IsZero() {
				t.Fatalf("aborted StartedAt = %s, present = %t", startedAt, started)
			}
			processID := outcomes[0].Admission().Relation().ProcessID()
			if _, published := engine.Process(processID); published {
				t.Fatal("aborted Process was published")
			}
			assertNoPendingProcessStarts(t, engine)
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProcessStartOutcomeReportsChildInitializationFailure(t *testing.T) {
	childDeployment := failingStartDeployment(t, errors.New("child cannot initialize"))
	parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
	var mu sync.Mutex
	var outcomes []ProcessStartOutcome
	engine, err := NewEngine(EngineConfig{
		DeploymentResolver: deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment},
		ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(
			_ context.Context,
			outcome ProcessStartOutcome,
		) error {
			mu.Lock()
			outcomes = append(outcomes, outcome)
			mu.Unlock()
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(struct{}{})
	parent, err := engine.Start(t.Context(), parentDeployment, input)
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, parent))
	if output.Failures != 1 || len(output.FailureCodes) != 1 ||
		output.FailureCodes[0] != "engine.process.start.failed" {
		t.Fatalf("parent output = %#v", output)
	}
	mu.Lock()
	got := append([]ProcessStartOutcome(nil), outcomes...)
	mu.Unlock()
	if len(got) != 2 || got[1].Status() != ProcessStartOutcomeStatusAborted {
		t.Fatalf("outcomes = %#v", got)
	}
	parentID, child := got[1].Admission().Relation().ParentID()
	failure, failed := got[1].Failure()
	if !child || parentID != parent.ID() || !failed || failure.Code() != "engine.process.start.failed" {
		t.Fatalf("child outcome = %#v", got[1])
	}
	if _, published := engine.Process(got[1].Admission().Relation().ProcessID()); published {
		t.Fatal("aborted child was published")
	}
	assertNoPendingProcessStarts(t, engine)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectingStartedProcessOutcomePreventsPublication(t *testing.T) {
	rejection := errors.New("outcome was not accepted")
	engine, err := NewEngine(EngineConfig{
		ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(
			context.Context,
			ProcessStartOutcome,
		) error {
			return rejection
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	process, err := engine.Start(t.Context(), deployment, input)
	if process != nil || !errors.Is(err, rejection) {
		t.Fatalf("Start process=%v error=%v", process, err)
	}
	assertNoPendingProcessStarts(t, engine)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectingAbortedProcessOutcomePreservesBothFailures(t *testing.T) {
	initializationErr := errors.New("definition cannot initialize")
	acknowledgmentErr := errors.New("outcome was not accepted")
	engine, err := NewEngine(EngineConfig{
		ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(
			_ context.Context,
			outcome ProcessStartOutcome,
		) error {
			if outcome.Status() != ProcessStartOutcomeStatusAborted {
				t.Fatalf("outcome status = %s, want aborted", outcome.Status())
			}
			return acknowledgmentErr
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := failingStartDeployment(t, initializationErr)
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	process, err := engine.Start(t.Context(), deployment, input)
	if process != nil || !errors.Is(err, initializationErr) || !errors.Is(err, acknowledgmentErr) {
		t.Fatalf("Start process=%v error=%v", process, err)
	}
	assertNoPendingProcessStarts(t, engine)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectingStartedChildOutcomePreventsChildPublication(t *testing.T) {
	for _, mode := range []struct {
		name       string
		durability TreeDurability
	}{
		{name: "ephemeral"},
		{name: "durable", durability: &recordingTreeDurability{}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			childDeployment := newChildTestDeployment(t)
			parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
			rejection := errors.New("child outcome was not accepted")
			var childID ProcessID
			engine, err := NewEngine(EngineConfig{
				TreeDurability:     mode.durability,
				DeploymentResolver: deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment},
				ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(
					_ context.Context,
					outcome ProcessStartOutcome,
				) error {
					if outcome.Admission().Relation().IsRoot() {
						return nil
					}
					childID = outcome.Admission().Relation().ProcessID()
					return rejection
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := EncodeInput(struct{}{})
			parent, err := engine.Start(t.Context(), parentDeployment, input)
			if err != nil {
				t.Fatal(err)
			}
			output := childTestResult(t, mustAwait(t, parent))
			if output.Failures != 1 || len(output.FailureCodes) != 1 ||
				output.FailureCodes[0] != "engine.child.start_outcome.unacknowledged" {
				t.Fatalf("parent output = %#v", output)
			}
			if !childID.Valid() {
				t.Fatal("child outcome was not proposed")
			}
			if _, published := engine.Process(childID); published {
				t.Fatal("unacknowledged child was published")
			}
			assertNoPendingProcessStarts(t, engine)
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProcessStartOutcomeAcknowledgerPanicAndTypedNilAreContained(t *testing.T) {
	var typedNil ProcessStartOutcomeAcknowledgerFunc
	if _, err := NewEngine(EngineConfig{ProcessStartOutcomeAcknowledger: typedNil}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("typed-nil error = %v, want %v", err, ErrInvalidEngineConfig)
	}
	engine, err := NewEngine(EngineConfig{
		ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(
			context.Context,
			ProcessStartOutcome,
		) error {
			panic("outcome panic")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	if process, err := engine.Start(t.Context(), deployment, input); process != nil || err == nil {
		t.Fatalf("panicking acknowledger process=%v error=%v", process, err)
	}
	assertNoPendingProcessStarts(t, engine)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEngineCannotCloseWhileProcessStartOutcomeIsPending(t *testing.T) {
	acknowledging := make(chan struct{})
	release := make(chan struct{})
	engine, err := NewEngine(EngineConfig{
		ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(
			context.Context,
			ProcessStartOutcome,
		) error {
			close(acknowledging)
			<-release
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	type startResult struct {
		process *Process
		err     error
	}
	result := make(chan startResult, 1)
	go func() {
		process, startErr := engine.Start(t.Context(), deployment, input)
		result <- startResult{process: process, err: startErr}
	}()
	<-acknowledging
	if err := engine.Close(); !errors.Is(err, ErrEngineHasActiveProcesses) {
		t.Fatalf("Close error = %v, want %v", err, ErrEngineHasActiveProcesses)
	}
	close(release)
	started := <-result
	if started.err != nil {
		t.Fatal(started.err)
	}
	_ = mustAwait(t, started.process)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedAdmissionProducesNoProcessStartOutcome(t *testing.T) {
	var outcomeCount int
	engine, err := NewEngine(EngineConfig{
		ProcessAdmitter: ProcessAdmitterFunc(func(context.Context, ProcessAdmission) error {
			return errors.New("not admitted")
		}),
		ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(
			context.Context,
			ProcessStartOutcome,
		) error {
			outcomeCount++
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	if process, err := engine.Start(t.Context(), deployment, input); process != nil ||
		!errors.Is(err, ErrProcessAdmissionRejected) {
		t.Fatalf("Start process=%v error=%v", process, err)
	}
	if outcomeCount != 0 {
		t.Fatalf("outcomes = %d, want 0", outcomeCount)
	}
	assertNoPendingProcessStarts(t, engine)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

type initializationFailureStage uint8

const (
	failDefinitionStart initializationFailureStage = iota + 1
	failInitialSnapshot
	failInitialRestore
)

type failingInitializationDefinition struct {
	Definition
	stage initializationFailureStage
	err   error
}

func (f *failingInitializationDefinition) Start(input Input) (Execution, error) {
	if f.stage == failDefinitionStart {
		return nil, f.err
	}
	execution, err := f.Definition.Start(input)
	if err != nil || f.stage != failInitialSnapshot {
		return execution, err
	}
	return &failingInitialSnapshotExecution{Execution: execution, err: f.err}, nil
}

func (f *failingInitializationDefinition) Restore(state ExecutionState) (Execution, error) {
	if f.stage == failInitialRestore {
		return nil, f.err
	}
	return f.Definition.Restore(state)
}

type failingInitialSnapshotExecution struct {
	Execution
	err error
}

func (f *failingInitialSnapshotExecution) Snapshot() (ExecutionState, error) {
	return ExecutionState{}, f.err
}

func failingStartDeployment(t *testing.T, startErr error) Deployment {
	return failingInitializationDeployment(t, failDefinitionStart, startErr)
}

func failingInitializationDeployment(
	t *testing.T,
	stage initializationFailureStage,
	initializationErr error,
) Deployment {
	t.Helper()
	base := newChildTestDeployment(t)
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: &failingInitializationDefinition{
			Definition: base.Definition(), stage: stage, err: initializationErr,
		},
		Dispatcher:           base.effectDispatcher(),
		ImplementationDigest: ComputeDigest([]byte("failing-initialization-implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("failing-initialization-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func assertNoPendingProcessStarts(t *testing.T, engine *Engine) {
	t.Helper()
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if len(engine.startReservations) != 0 || len(engine.childStartReservations) != 0 {
		t.Fatalf(
			"pending starts = %d, pending children = %d",
			len(engine.startReservations), len(engine.childStartReservations),
		)
	}
}
