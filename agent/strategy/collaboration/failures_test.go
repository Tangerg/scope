package collaboration

import (
	"context"
	"errors"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func TestWorkerFailuresRemainCoordinatorFacts(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "execution", true: "start"}[unavailable], func(t *testing.T) {
			worker := transformed("test.failing", func(context.Context, string) (string, error) { return "", errors.New("worker failure") })
			definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
				if turn.Number == 1 {
					return Decision{Mode: Wait, State: turn.State, Tasks: []TaskRequest{request("work", "test.failing", "x")}}, nil
				}
				if turn.Number != 2 || len(turn.Tasks) != 1 || turn.Tasks[0].Start == nil {
					return Decision{}, errors.New("missing task fact")
				}
				if unavailable {
					if _, failed := turn.Tasks[0].Start.Failure(); !failed || turn.Tasks[0].Outcome != nil {
						return Decision{}, errors.New("failed admission fabricated a child")
					}
				} else if turn.Tasks[0].Outcome == nil || turn.Tasks[0].Outcome.Result().Status() != agent.StatusFailed {
					return Decision{}, errors.New("failed execution lost its outcome")
				}
				return finish(turn, "failure handled"), nil
			}, worker)
			if unavailable {
				delete(deployments, worker.DeploymentRef())
			}
			_, process := run(t, definition, deployments, nil)
			if got := completed(t, process); got != "failure handled" {
				t.Fatal(got)
			}
		})
	}
}

func TestCoordinatorFailuresAndFiniteBoundsStopCollaboration(t *testing.T) {
	for _, mode := range []string{"start", "execution", "turn limit", "task limit", "reused key"} {
		t.Run(mode, func(t *testing.T) {
			definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
				if mode == "execution" {
					return Decision{}, errors.New("coordinator failure")
				}
				if mode == "task limit" || mode == "reused key" {
					key := "work"
					if mode == "task limit" && turn.Number > 1 {
						key = "second"
					}
					return Decision{Mode: Wait, State: turn.State, Tasks: []TaskRequest{request(key, "test.echo", "x")}}, nil
				}
				return Decision{Mode: Continue, State: turn.State}, nil
			}, echo())
			config := definition.config
			if mode == "turn limit" {
				config.MaxTurns = 2
			}
			if mode == "task limit" {
				config.MaxTasks, config.MaxConcurrentTasks = 1, 1
			}
			definition = require(NewDefinition(config))
			if mode == "start" {
				delete(deployments, config.Coordinator.Deployment.DeploymentRef())
			}
			_, process := run(t, definition, deployments, nil)
			result := require(process.Await(t.Context()))
			if result.Status() != agent.StatusFailed {
				t.Fatal(result.Status())
			}
			reason := result.Termination().Reason()
			want := map[string]string{"start": "deployment unavailable", "execution": "coordinator failure", "turn limit": "turn limit reached", "task limit": "bound exceeded", "reused key": "reused task key"}[mode]
			if !strings.Contains(reason, want) {
				t.Fatalf("lost failure cause: %s", reason)
			}
		})
	}
}

func TestCompletionCancelsOutstandingTask(t *testing.T) {
	definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
		if turn.Number == 1 {
			return Decision{Mode: Continue, State: turn.State, Tasks: []TaskRequest{request("background", "test.gate", "wait")}}, nil
		}
		return finish(turn, "done"), nil
	}, gate())
	engine, process := run(t, definition, deployments, nil)
	completed(t, process)
	tree := require(engine.InspectTree(t.Context(), process.ID()))
	for _, fact := range tree.Processes {
		if fact.Snapshot.DeploymentRef().Name() == "test.gate" && fact.Snapshot.Status() != agent.StatusCanceled {
			t.Fatal("completion left a running descendant")
		}
	}
}
