package collaboration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/coordination"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

func require[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func binding(definition agent.Definition) agent.Deployment {
	return require(agent.NewDeployment(agent.DeploymentConfig{Definition: definition,
		ImplementationDigest: agent.ComputeDigest([]byte("collaboration-test-code")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(definition.Descriptor().Name()))}))
}

func transformed[I, O any](name string, transform func(context.Context, I) (O, error)) agent.Deployment {
	stage := require(workflow.Transform(name+".transform", transform))
	return binding(require(workflow.NewDefinition(workflow.DefinitionConfig{Name: name, Description: name, Stages: []workflow.Stage{stage}})))
}

func workerConfig(deployment agent.Deployment) WorkerConfig {
	return WorkerConfig{Deployment: deployment, Budget: agent.Budget{Steps: 32, Effects: 16, Signals: 32}}
}

func fixture(coordinator func(context.Context, Turn) (Decision, error), workers ...agent.Deployment) (*Definition, resolver) {
	config, deployments := fixtureConfig(coordinator, workers...)
	return require(NewDefinition(config)), deployments
}

func fixtureConfig(coordinator func(context.Context, Turn) (Decision, error), workers ...agent.Deployment) (DefinitionConfig, resolver) {
	decision := transformed("test.coordinator", coordinator)
	config := DefinitionConfig{Name: "test.collaboration", Description: "Coordinate finite tasks.", Coordinator: workerConfig(decision),
		StateSchema: require(agent.SchemaFor[string]()), OutputSchema: require(agent.SchemaFor[string]()),
		MaxTurns: 8, MaxTasks: 8, MaxConcurrentTasks: 4, MaxControlsPerTurn: 4}
	deployments := resolver{decision.DeploymentRef(): decision}
	for _, worker := range workers {
		config.Workers = append(config.Workers, workerConfig(worker))
		deployments[worker.DeploymentRef()] = worker
	}
	return config, deployments
}

type resolver map[agent.DeploymentRef]agent.Deployment

func (r resolver) Resolve(ref agent.DeploymentRef) (agent.Deployment, error) {
	if deployment, found := r[ref]; found {
		return deployment, nil
	}
	return agent.Deployment{}, errors.New("deployment unavailable")
}

func input(value string) agent.Input { return require(agent.EncodeInput(value)) }
func request(key, worker, value string) TaskRequest {
	return TaskRequest{Key: require(agent.ParseChildKey(key)), Worker: worker, Input: input(value)}
}
func finish(turn Turn, text string) Decision {
	output := require(agent.EncodeOutput(text))
	return Decision{Mode: Complete, State: turn.State, Output: &output}
}
func echo() agent.Deployment {
	return transformed("test.echo", func(_ context.Context, text string) (string, error) { return "echo: " + text, nil })
}
func gate() agent.Deployment {
	schema := require(agent.SchemaFor[string]())
	return binding(require(coordination.NewInputGate(coordination.InputGateConfig{Name: "test.gate", Description: "Receive input.", RequestSchema: schema, AnswerSchema: schema})))
}
func run(t *testing.T, definition *Definition, deployments resolver, durability agent.TreeDurability) (*agent.Engine, *agent.Process) {
	t.Helper()
	engine := require(agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployments, TreeDurability: durability}))
	t.Cleanup(func() {
		if err := engine.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	process := require(engine.Start(t.Context(), binding(definition), input("initial")))
	return engine, process
}
func completed(t *testing.T, process *agent.Process) string {
	t.Helper()
	result := require(process.Await(t.Context()))
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status=%s termination=%+v", result.Status(), result.Termination())
	}
	if err := process.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	output, _ := result.Output()
	return require(output.Decode[string]())
}

func TestBackgroundContinueControlAndDrain(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprint(durable), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
					switch turn.Number {
					case 1:
						return Decision{Mode: Continue, State: input("working"), Tasks: []TaskRequest{request("background", "test.gate", "wait")}}, nil
					case 2:
						if len(turn.Tasks) != 1 || turn.Tasks[0].Start == nil || turn.Tasks[0].Outcome != nil || require(turn.State.Decode[string]()) != "working" {
							return Decision{}, errors.New("coordinator did not continue beside the active task")
						}
						signal := require(agent.NewSignalRequest(require(agent.ParseSignalID("signal:steer")), agent.WaitID{}, []byte(`"new direction"`)))
						reason := "No longer needed."
						return Decision{Mode: Wait, State: turn.State, Controls: []Control{
							{Task: turn.Tasks[0].Request.Key, Signal: &signal}, {Task: turn.Tasks[0].Request.Key, CancelReason: &reason},
						}}, nil
					case 3:
						if len(turn.Controls) != 2 || turn.Controls[0].Result == nil || turn.Controls[1].Result == nil ||
							turn.Tasks[0].Outcome == nil || turn.Tasks[0].Outcome.Result().Status() != agent.StatusCanceled {
							return Decision{}, errors.New("control receipts or drained cancellation missing")
						}
						if _, failed := turn.Controls[1].Result.Failure(); failed {
							return Decision{}, errors.New("cancel rejected")
						}
						return finish(turn, "continued, controlled, drained"), nil
					default:
						return Decision{}, errors.New("unexpected turn")
					}
				}, gate())
				var durability agent.TreeDurability
				if durable {
					durability = agenttest.NewMemoryTreeDurability()
				}
				_, process := run(t, definition, deployments, durability)
				if got := completed(t, process); got != "continued, controlled, drained" {
					t.Fatal(got)
				}
			})
		})
	}
}

func TestCompletedTaskFollowUp(t *testing.T) {
	definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
		switch turn.Number {
		case 1:
			return Decision{Mode: Wait, State: turn.State, Tasks: []TaskRequest{request("draft", "test.echo", "draft")}}, nil
		case 2:
			if turn.Tasks[0].Outcome == nil {
				return Decision{}, errors.New("missing draft")
			}
			output, _ := turn.Tasks[0].Outcome.Result().Output()
			return Decision{Mode: Wait, State: turn.State, Tasks: []TaskRequest{request("revision", "test.echo", require(output.Decode[string]())+" revised")}}, nil
		case 3:
			output, _ := turn.Tasks[1].Outcome.Result().Output()
			return finish(turn, require(output.Decode[string]())), nil
		default:
			return Decision{}, errors.New("unexpected turn")
		}
	}, echo())
	_, process := run(t, definition, deployments, nil)
	if got := completed(t, process); got != "echo: echo: draft revised" {
		t.Fatal(got)
	}
}

func TestAddressedInputWakesWaitingCollaboration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
			if turn.Number == 1 {
				return Decision{Mode: Wait, State: turn.State, Tasks: []TaskRequest{request("input", "test.gate", "instruction")}}, nil
			}
			if turn.Number != 2 || turn.Tasks[0].Outcome == nil {
				return Decision{}, errors.New("missing input outcome")
			}
			output, _ := turn.Tasks[0].Outcome.Result().Output()
			signal := require(output.Decode[agent.Signal]())
			return finish(turn, signal.ID().String()+":"+string(signal.Payload())), nil
		}, gate())
		store := agenttest.NewMemoryTreeDurability()
		engine, process := run(t, definition, deployments, store)
		synctest.Wait()
		tree := require(engine.InspectTree(t.Context(), process.ID()))
		var recipient agent.ProcessID
		var wait agent.WaitID
		for _, fact := range tree.Processes {
			if fact.Snapshot.DeploymentRef().Name() == "test.gate" {
				recipient = fact.Snapshot.ProcessID()
				wait, _ = fact.Snapshot.WaitID()
			}
		}
		if !recipient.Valid() || !wait.Valid() {
			t.Fatal("input gate did not wait")
		}
		child, found := engine.Process(recipient)
		if !found {
			t.Fatal("input process absent")
		}
		signal := require(agent.NewSignalRequest(require(agent.ParseSignalID("signal:answer")), wait, []byte(`"proceed"`)))
		if accepted, err := child.DeliverSignals(t.Context(), signal); err != nil || !accepted {
			t.Fatalf("delivery=%t %v", accepted, err)
		}
		if got := completed(t, process); got != `signal:answer:"proceed"` {
			t.Fatal(got)
		}
	})
}

func TestDefinitionConformance(t *testing.T) {
	definition, _ := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{Definition: definition, Input: input("initial")})
}
