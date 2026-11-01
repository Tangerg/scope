package planning_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

type fixtureGatedDefinition struct {
	run.Definition
	gate <-chan struct{}
}

func (f fixtureGatedDefinition) Start(input run.Payload) (run.Execution, error) {
	execution, err := f.Definition.Start(input)
	return fixtureGatedExecution{execution, f.gate}, err
}
func (f fixtureGatedDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	execution, err := f.Definition.Restore(ctx, state)
	return fixtureGatedExecution{execution, f.gate}, err
}

type fixtureGatedExecution struct {
	run.Execution
	gate <-chan struct{}
}

func (f fixtureGatedExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	s, err := f.Snapshot()
	if err != nil {
		return run.Transition{}, err
	}
	var state struct {
		Phase string `json:"phase"`
		Child *struct {
			ProcessID string `json:"process_id"`
		} `json:"child"`
	}
	if err := jsonv2.Unmarshal(s.Payload(), &state); err != nil {
		return run.Transition{}, err
	}
	// The only binding is child-bound, so an awaited Action without child
	// progress is awaiting its start.
	if state.Phase == "awaiting_action" && state.Child == nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return run.Transition{}, ctx.Err()
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
	child, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{Definition: childDef, ImplementationDigest: run.ComputeDigest([]byte("child")), ConfigurationDigest: run.ComputeDigest([]byte("config"))})
	if err != nil {
		t.Fatal(err)
	}
	done := mustCondition(t, "world.done", planning.TruthTrue)
	action := mustAction(t, planning.ActionConfig{Name: "action.delegate", Description: "Run a child.", Effects: []planning.Condition{done}})
	budget := run.Budget{Steps: run.NewQuota(10), Effects: run.NewQuota(10), Signals: run.NewQuota(10)}
	binding, err := planning.NewChildBinding(planning.ChildBindingConfig{Action: action, Deployment: child, Budget: budget})
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
	root, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{Definition: fixtureGatedDefinition{def, gate}, Dispatcher: dispatcher, ImplementationDigest: run.ComputeDigest([]byte("root")), ConfigurationDigest: run.ComputeDigest([]byte("config"))})
	if err != nil {
		t.Fatal(err)
	}
	result := runManaged(t, run.Config{Store: run.NewMemoryStore(), EventListeners: []run.EventListener{run.EventListenerFunc(func(_ context.Context, event run.Event) {
		if event.Relation().Depth() == 1 && event.Name() == run.EventProcessFinished {
			once.Do(func() { close(gate) })
		}
	})}}, root)
	if result.Termination().Status() != run.StatusCompleted {
		f, _ := result.Termination().Failure()
		t.Fatalf("status=%s failure=%s message=%s", result.Termination().Status(), f.Code(), f.Message())
	}
}
