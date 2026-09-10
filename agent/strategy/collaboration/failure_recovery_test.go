package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

func TestCoordinatorFailureSurvivesRecoveryAndDrainsWorkers(t *testing.T) {
	for _, mode := range []string{"start", "execution", "panic"} {
		t.Run(mode, func(t *testing.T) {
			kind, code, message := agent.FailureKindExecution, "execution.step.failed", strings.Repeat("x", 4096)
			switch mode {
			case "start":
				kind = agent.FailureKindExternal
				code, message = "engine.child.deployment_unavailable", "deployment unavailable"
			case "panic":
				kind = agent.FailureKindPanic
				message = "execution panicked: " + strings.Repeat("x", 4096-len("execution panicked: "))
			case "execution":
				const prefix = `transform "test.coordinator.transform": `
				message = prefix + strings.Repeat("x", 4096-len(prefix))
			}
			definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
				if turn.Number == 1 {
					return Decision{Mode: Continue, State: turn.State, Tasks: []TaskRequest{request("background", "test.gate", "wait")}}, nil
				}
				if mode == "panic" {
					panic(strings.TrimPrefix(message, "execution panicked: "))
				}
				return Decision{}, errors.New(strings.TrimPrefix(message, `transform "test.coordinator.transform": `))
			}, gate())
			if mode == "start" {
				delete(deployments, definition.config.Coordinator.Deployment.DeploymentRef())
			}
			store := agenttest.NewMemoryTreeDurability()
			_, process := run(t, definition, deployments, store)
			result := require(process.Await(t.Context()))
			if err := process.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != kind || failure.Code() != code || failure.Message() != message {
				t.Fatalf("coordinator failure changed: kind=%s code=%s message bytes=%d", failure.Kind(), failure.Code(), len(failure.Message()))
			}
			head, present, err := store.LoadTree(t.Context(), process.ID())
			if err != nil || !present {
				t.Fatalf("durable tree missing: %v", err)
			}
			var state agent.ExecutionState
			for _, snapshot := range head.ProcessSnapshots() {
				if snapshot.ProcessID() == process.ID() {
					state = snapshot.CommittedExecutionState()
				}
				if snapshot.DeploymentRef().Name() == "test.gate" && snapshot.Status() != agent.StatusCanceled {
					t.Fatal("coordinator failure left background work running")
				}
			}
			var decoded executionState
			if err := json.Unmarshal(state.Payload(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Phase != "failed" {
				t.Fatalf("failed turn was not retained: %s", decoded.Phase)
			}
			if _, err := definition.Restore(state); err != nil {
				t.Fatal(err)
			}
			for name, mutate := range map[string]func(*executionState){
				"missing start":   func(state *executionState) { state.Turn.Start = nil },
				"completed phase": func(state *executionState) { state.Phase = phaseCompleted },
				"decision mode":   func(state *executionState) { state.Mode = Continue },
				"changed state":   func(state *executionState) { state.State = input("forged") },
			} {
				t.Run(name, func(t *testing.T) {
					var altered executionState
					if err := json.Unmarshal(state.Payload(), &altered); err != nil {
						t.Fatal(err)
					}
					mutate(&altered)
					if _, err := definition.Restore(require(agent.NewExecutionState(stateKind, require(json.Marshal(altered))))); !errors.Is(err, ErrInvalidState) {
						t.Fatalf("contradictory failure state accepted: %v", err)
					}
				})
			}
			restoredEngine := require(agent.NewEngine(agent.EngineConfig{TreeDurability: store, DeploymentResolver: deployments}))
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
