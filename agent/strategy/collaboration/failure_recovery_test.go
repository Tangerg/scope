package collaboration

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func TestCoordinatorFailureSurvivesRecoveryAndDrainsWorkers(t *testing.T) {
	for _, mode := range []string{"start", "execution", "panic"} {
		t.Run(mode, func(t *testing.T) {
			kind, code, message := agent.FailureKindExecution, "execution.step.failed", strings.Repeat("x", 4096)
			switch mode {
			case "start":
				kind = agent.FailureKindExternal
				code, message = "engine.child.admission.rejected", "agent: process admission rejected: coordinator refused"
			case "panic":
				kind = agent.FailureKindPanic
				message = "agent: Execution.Step panicked: " + strings.Repeat("x", 4096-len("agent: Execution.Step panicked: "))
			case "execution":
				const prefix = `transform "test.coordinator.transform": `
				message = prefix + strings.Repeat("x", 4096-len(prefix))
			}
			definition := fixture(func(_ context.Context, turn Turn) (Decision, error) {
				if turn.Number == 1 {
					return Decision{Mode: ModeContinue, State: turn.State, Tasks: []TaskRequest{request("background", "test.gate", "wait")}}, nil
				}
				if mode == "panic" {
					panic(strings.TrimPrefix(message, "agent: Execution.Step panicked: "))
				}
				return Decision{}, errors.New(strings.TrimPrefix(message, `transform "test.coordinator.transform": `))
			}, gate())
			store := agent.NewMemoryTreeCommitter()
			config := agent.EngineConfig{TreeCommitter: store}
			if mode == "start" {
				config.ProcessAdmitter = agent.ProcessAdmitterFunc(func(_ context.Context, admission agent.ProcessAdmission) error {
					if key, child := admission.Relation().ChildKey(); child && key.String() == "collaboration.turn.2" {
						return errors.New("coordinator refused")
					}
					return nil
				})
			}
			_, process := runWith(t, definition, config)
			result := require(process.Await(t.Context()))
			if err := process.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != kind || failure.Code() != code || failure.Message() != message {
				t.Fatalf("coordinator failure changed: kind=%s code=%s message bytes=%d", failure.Kind(), failure.Code(), len(failure.Message()))
			}
			head, present, err := store.LoadTree(t.Context(), process.Relation().ProcessID())
			if err != nil || !present {
				t.Fatalf("durable tree missing: %v", err)
			}
			var state agent.ExecutionState
			for _, snapshot := range head.ProcessSnapshots() {
				if snapshot.Relation().ProcessID() == process.Relation().ProcessID() {
					state = snapshot.CommittedExecutionState()
				}
				if snapshot.DeploymentRef().Name() == "test.gate" && snapshot.Status() != agent.StatusCanceled {
					t.Fatal("coordinator failure left background work running")
				}
			}
			var decoded executionState
			if err := jsonv2.Unmarshal(state.Payload(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Turn.Outcome != nil || mode == "start" && decoded.Turn.Start != nil {
				t.Fatal("terminal state repeats the failed turn the Engine owns")
			}
			if _, err := definition.Restore(t.Context(), state); err != nil {
				t.Fatal(err)
			}
			mutations := map[string]func(*executionState){
				"retained initial state": func(state *executionState) { state.InitialState = input("forged") },
				"invalid turn state":     func(state *executionState) { state.Turn.State = require(agent.EncodePayload(42)) },
			}
			for name, mutate := range mutations {
				t.Run(name, func(t *testing.T) {
					var altered executionState
					if err := jsonv2.Unmarshal(state.Payload(), &altered); err != nil {
						t.Fatal(err)
					}
					mutate(&altered)
					if _, err := definition.Restore(t.Context(), require(agent.ParseExecutionState(stateKind, require(jsonv2.Marshal(altered))))); !errors.Is(err, ErrInvalidExecutionState) {
						t.Fatalf("contradictory failure state accepted: %v", err)
					}
				})
			}
			restoredEngine := require(agent.NewEngine(agent.EngineConfig{TreeCommitter: store}))
			t.Cleanup(func() {
				if closeErr := restoredEngine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			restored := require(restoredEngine.RestoreTree(t.Context(), binding(definition), head))
			recovered := require(restored.Await(t.Context()))
			if failure, failed := recovered.Termination().Failure(); !failed || failure.Kind() != kind || failure.Code() != code || failure.Message() != message {
				t.Fatalf("restored failure changed: %+v", recovered.Termination())
			}
		})
	}
}
