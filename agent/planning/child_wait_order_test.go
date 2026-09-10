package planning_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/workflow"
)

type fixtureGatedDefinition struct {
	agent.Definition
	gate <-chan struct{}
}

func (f fixtureGatedDefinition) Start(input agent.Input) (agent.Execution, error) {
	execution, err := f.Definition.Start(input)
	return fixtureGatedExecution{execution, f.gate}, err
}
func (f fixtureGatedDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := f.Definition.Restore(state)
	return fixtureGatedExecution{execution, f.gate}, err
}

type fixtureGatedExecution struct {
	agent.Execution
	gate <-chan struct{}
}

func (f fixtureGatedExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	s, err := f.Snapshot()
	if err != nil {
		return agent.Transition{}, err
	}
	var state struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(s.Payload(), &state); err != nil {
		return agent.Transition{}, err
	}
	if state.Phase == "awaiting_child_start" {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return agent.Transition{}, ctx.Err()
		}
	}
	return f.Execution.Step(ctx, signals)
}
func TestPlanningAlreadyCompletedChild(t *testing.T) {
	stage, err := workflow.Transform("identity", func(_ context.Context, v struct{}) (struct{}, error) { return v, nil })
	if err != nil {
		t.Fatal(err)
	}
	childDef, err := workflow.NewDefinition(workflow.DefinitionConfig{Name: "fixture.child", Description: "Complete immediately.", Stages: []workflow.Stage{stage}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := agent.NewDeployment(agent.DeploymentConfig{Definition: childDef, ImplementationDigest: agent.ComputeDigest([]byte("child")), ConfigurationDigest: agent.ComputeDigest([]byte("config"))})
	if err != nil {
		t.Fatal(err)
	}
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{Name: "action.delegate", Description: "Run a child.", Effects: []planning.Condition{done}})
	budget := agent.Budget{Steps: 10, Effects: 10, Signals: 10}
	binding, err := planning.NewChildBinding(planning.ChildBindingConfig{Action: action, DeploymentRef: child.DeploymentRef(), Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	def := newManagedDefinition(t, managedDeploymentConfig{goal: mustGoal(t, done), bindings: []planning.ActionBinding{binding}})
	dispatcher, err := planning.NewDispatcher(def, planning.DispatcherConfig{Sensor: newManagedWorld(t)})
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var once sync.Once
	root, err := agent.NewDeployment(agent.DeploymentConfig{Definition: fixtureGatedDefinition{def, gate}, Dispatcher: dispatcher, ImplementationDigest: agent.ComputeDigest([]byte("root")), ConfigurationDigest: agent.ComputeDigest([]byte("config"))})
	if err != nil {
		t.Fatal(err)
	}
	result := runManaged(t, agent.EngineConfig{DeploymentResolver: managedResolver{child.DeploymentRef(): child}, EventListeners: []agent.EventListener{agent.EventListenerFunc(func(_ context.Context, event agent.Event) {
		if event.Relation().Depth() == 1 && event.Name() == agent.EventProcessFinished {
			once.Do(func() { close(gate) })
		}
	})}}, root)
	if result.Status() != agent.StatusCompleted {
		f, _ := result.Termination().Failure()
		t.Fatalf("status=%s failure=%s message=%s", result.Status(), f.Code(), f.Message())
	}
}
