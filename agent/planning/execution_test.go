package planning_test

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/planning/goap"
)

func TestManagedPlanningReobservesAndReplansAfterEveryAction(t *testing.T) {
	ready := mustCondition(t, "world.ready", planning.True)
	done := mustCondition(t, "world.done", planning.True)
	prepare := mustAction(t, planning.ActionConfig{
		Name: "action.prepare", Description: "Prepare the world for completion.", Effects: []planning.Condition{ready},
	})
	finish := mustAction(t, planning.ActionConfig{
		Name: "action.finish", Description: "Finish the prepared work.",
		Preconditions: []planning.Condition{ready}, Effects: []planning.Condition{done},
	})
	world := newManagedWorld(t)
	bindings := []planning.ActionBinding{
		mustDispatcherBinding(t, prepare), mustDispatcherBinding(t, finish),
	}
	deployment := newManagedDeployment(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: bindings, sensor: world,
		executors: map[string]planning.ActionExecutor{
			"action.prepare": world.apply(prepare), "action.finish": world.apply(finish),
		},
	})

	result := runManaged(t, agent.EngineConfig{}, deployment)
	output := managedOutput(t, result)
	if output.Outcome != planning.OutcomeAchieved || output.PlanningPasses != 2 ||
		!slices.Equal(attemptNames(output.Attempts), []string{"action.prepare", "action.finish"}) ||
		world.observationCount() != 3 {
		t.Fatalf("output = %#v, observations = %d", output, world.observationCount())
	}
	for _, attempt := range output.Attempts {
		if attempt.Status != planning.AttemptSucceeded {
			t.Fatalf("attempt = %#v", attempt)
		}
	}
}

func TestManagedPlanningExcludesUnconfirmedActionAndReplans(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	optimistic := mustAction(t, planning.ActionConfig{
		Name: "action.optimistic", Description: "Attempt the inexpensive completion path.",
		Effects: []planning.Condition{done}, Cost: planning.FixedCost(1),
	})
	fallback := mustAction(t, planning.ActionConfig{
		Name: "action.fallback", Description: "Use the reliable completion path.",
		Effects: []planning.Condition{done}, Cost: planning.FixedCost(2),
	})
	world := newManagedWorld(t)
	deployment := newManagedDeployment(t, managedDeploymentConfig{
		goal: mustGoal(t, done),
		bindings: []planning.ActionBinding{
			mustDispatcherBinding(t, optimistic), mustDispatcherBinding(t, fallback),
		},
		sensor: world,
		executors: map[string]planning.ActionExecutor{
			"action.optimistic": planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
				return planning.ActionSucceeded(), nil
			}),
			"action.fallback": world.apply(fallback),
		},
	})

	output := managedOutput(t, runManaged(t, agent.EngineConfig{}, deployment))
	if output.Outcome != planning.OutcomeAchieved || len(output.Attempts) != 2 ||
		output.Attempts[0].Status != planning.AttemptUnconfirmed ||
		output.Attempts[1].Status != planning.AttemptSucceeded {
		t.Fatalf("output = %#v", output)
	}
}

func TestManagedPlanningRecordsDefiniteFailureAndUsesFallback(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	primary := mustAction(t, planning.ActionConfig{
		Name: "action.primary", Description: "Attempt the primary completion path.",
		Effects: []planning.Condition{done}, Cost: planning.FixedCost(1),
	})
	fallback := mustAction(t, planning.ActionConfig{
		Name: "action.fallback", Description: "Use the fallback completion path.",
		Effects: []planning.Condition{done}, Cost: planning.FixedCost(2),
	})
	world := newManagedWorld(t)
	failed, err := planning.ActionFailed("primary service rejected the request")
	if err != nil {
		t.Fatal(err)
	}
	deployment := newManagedDeployment(t, managedDeploymentConfig{
		goal: mustGoal(t, done),
		bindings: []planning.ActionBinding{
			mustDispatcherBinding(t, primary), mustDispatcherBinding(t, fallback),
		},
		sensor: world,
		executors: map[string]planning.ActionExecutor{
			"action.primary": planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
				return failed, nil
			}),
			"action.fallback": world.apply(fallback),
		},
	})

	output := managedOutput(t, runManaged(t, agent.EngineConfig{}, deployment))
	if output.Outcome != planning.OutcomeAchieved || len(output.Attempts) != 2 ||
		output.Attempts[0].Status != planning.AttemptFailed ||
		output.Attempts[0].Diagnostic != "primary service rejected the request" ||
		output.Attempts[1].Status != planning.AttemptSucceeded {
		t.Fatalf("output = %#v", output)
	}
}

func TestManagedPlanningUsesSemanticCompletionOutcomes(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	t.Run("unreachable", func(t *testing.T) {
		world := newManagedWorld(t)
		deployment := newManagedDeployment(t, managedDeploymentConfig{
			goal: mustGoal(t, done), sensor: world,
		})
		output := managedOutput(t, runManaged(t, agent.EngineConfig{}, deployment))
		if output.Outcome != planning.OutcomeUnreachable || len(output.Attempts) != 0 || output.PlanningPasses != 1 {
			t.Fatalf("output = %#v", output)
		}
	})

	t.Run("stuck", func(t *testing.T) {
		only := mustAction(t, planning.ActionConfig{
			Name: "action.only", Description: "Attempt the only completion path.", Effects: []planning.Condition{done},
		})
		world := newManagedWorld(t)
		failed, err := planning.ActionFailed("definite refusal")
		if err != nil {
			t.Fatal(err)
		}
		deployment := newManagedDeployment(t, managedDeploymentConfig{
			goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, only)},
			sensor: world,
			executors: map[string]planning.ActionExecutor{
				"action.only": planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
					return failed, nil
				}),
			},
		})
		result := runManaged(t, agent.EngineConfig{}, deployment)
		output := managedOutput(t, result)
		if result.Status() != agent.StatusCompleted || output.Outcome != planning.OutcomeStuck ||
			len(output.Attempts) != 1 || output.Attempts[0].Status != planning.AttemptFailed {
			t.Fatalf("result status = %s, output = %#v", result.Status(), output)
		}
	})

	t.Run("attempt limit", func(t *testing.T) {
		first := mustAction(t, planning.ActionConfig{
			Name: "action.first", Description: "Attempt the first completion path.",
			Effects: []planning.Condition{done}, Cost: planning.FixedCost(1),
		})
		second := mustAction(t, planning.ActionConfig{
			Name: "action.second", Description: "Attempt the second completion path.",
			Effects: []planning.Condition{done}, Cost: planning.FixedCost(2),
		})
		world := newManagedWorld(t)
		failed, err := planning.ActionFailed("first path refused")
		if err != nil {
			t.Fatal(err)
		}
		deployment := newManagedDeployment(t, managedDeploymentConfig{
			goal: mustGoal(t, done),
			bindings: []planning.ActionBinding{
				mustDispatcherBinding(t, first), mustDispatcherBinding(t, second),
			},
			sensor: world,
			executors: map[string]planning.ActionExecutor{
				"action.first": planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
					return failed, nil
				}),
				"action.second": world.apply(second),
			},
			maxActionAttempts: 1,
		})
		output := managedOutput(t, runManaged(t, agent.EngineConfig{}, deployment))
		if output.Outcome != planning.OutcomeStuck || len(output.Attempts) != 1 ||
			output.Attempts[0].ActionName != "action.first" || world.truth("world.done") != planning.Unknown {
			t.Fatalf("output = %#v", output)
		}
	})
}

func TestManagedPlanningClassifiesObservationAndPlannerFailures(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	t.Run("observation", func(t *testing.T) {
		deployment := newManagedDeployment(t, managedDeploymentConfig{
			goal: mustGoal(t, done),
			sensor: planning.SensorFunc(func(context.Context, planning.SenseRequest) (planning.WorldState, error) {
				return planning.WorldState{}, errors.New("sensor unavailable")
			}),
		})
		result := runManaged(t, agent.EngineConfig{}, deployment)
		assertFailure(t, result, agent.FailureKindExternal, "planning.sensing.failed")
	})

	t.Run("planner contract", func(t *testing.T) {
		action := mustAction(t, planning.ActionConfig{
			Name: "action.finish", Description: "Finish the work.", Effects: []planning.Condition{done},
		})
		planned, err := planning.NewPlannedAction(action.Name())
		if err != nil {
			t.Fatal(err)
		}
		invalidCost, err := planning.NewPlan([]planning.PlannedAction{planned}, 99)
		if err != nil {
			t.Fatal(err)
		}
		world := newManagedWorld(t)
		deployment := newManagedDeployment(t, managedDeploymentConfig{
			goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
			sensor:    world,
			executors: map[string]planning.ActionExecutor{"action.finish": world.apply(action)},
			planner: planning.PlannerFunc(func(context.Context, planning.Problem) (planning.Plan, bool, error) {
				return invalidCost, true, nil
			}),
		})
		result := runManaged(t, agent.EngineConfig{}, deployment)
		assertFailure(t, result, agent.FailureKindContract, "planning.planner.contract")
	})
}

func TestManagedPlanningRestoresExactBoundaryState(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "action.finish", Description: "Finish the work.", Effects: []planning.Condition{done},
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
	})
	input, err := agent.EncodeInput(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	transition, err := execution.Step(context.Background(), nil)
	if err != nil || transition.Kind() != agent.TransitionKindContinue || len(transition.Effects()) != 1 {
		t.Fatalf("transition = %#v, error = %v", transition, err)
	}
	before, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(before)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if before.Kind() != after.Kind() || !bytes.Equal(before.Payload(), after.Payload()) {
		t.Fatalf("restored state differs\nbefore: %s\nafter:  %s", before.Payload(), after.Payload())
	}
}

func TestManagedPlanningUnknownActionRequiresExplicitResolution(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "action.finish", Description: "Finish the work.", Effects: []planning.Condition{done},
	})
	world := newManagedWorld(t)
	requestSeen := make(chan planning.ActionRequest, 1)
	deployment := newManagedDeployment(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
		sensor: world,
		executors: map[string]planning.ActionExecutor{
			"action.finish": planning.ActionExecutorFunc(func(_ context.Context, request planning.ActionRequest) (planning.ActionResult, error) {
				requestSeen <- request
				return planning.ActionResult{}, errors.New("connection lost after send")
			}),
		},
	})
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil && !errors.Is(closeErr, agent.ErrEngineHasActiveProcesses) {
			t.Errorf("Close: %v", closeErr)
		}
	})
	input, _ := agent.EncodeInput(struct{}{})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	request := <-requestSeen
	queryContext, cancelQuery := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelQuery()
	for {
		unknown, queryErr := process.UnknownEffectIDs(queryContext)
		if queryErr != nil {
			t.Fatalf("query unknown Action Effect: %v", queryErr)
		}
		if len(unknown) == 1 {
			if unknown[0] != request.EffectID {
				t.Fatalf("unknown EffectID = %s, request = %s", unknown[0], request.EffectID)
			}
			break
		}
		runtime.Gosched()
	}
	world.applyState(t, action)
	settlement, err := planning.NewActionSettlement(request.EffectID, planning.ActionSucceeded())
	if err != nil {
		t.Fatal(err)
	}
	if resolveEffectErr := process.ResolveUnknownEffect(context.Background(), settlement); resolveEffectErr != nil {
		t.Fatal(resolveEffectErr)
	}
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if output := managedOutput(t, result); output.Outcome != planning.OutcomeAchieved {
		t.Fatalf("output = %#v", output)
	}
}

func TestManagedPlanningExecutesChildProcessAction(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	world := newManagedWorld(t)
	childAction := mustAction(t, planning.ActionConfig{
		Name: "action.child_finish", Description: "Complete the work inside the child Process.",
		Effects: []planning.Condition{done},
	})
	childDeployment := newManagedDeployment(t, managedDeploymentConfig{
		name: "planning.child", goal: mustGoal(t, done),
		bindings: []planning.ActionBinding{mustDispatcherBinding(t, childAction)}, sensor: world,
		executors: map[string]planning.ActionExecutor{"action.child_finish": world.apply(childAction)},
	})
	delegate := mustAction(t, planning.ActionConfig{
		Name: "action.delegate", Description: "Delegate completion to an exact child Deployment.",
		Effects: []planning.Condition{done},
	})
	budget, err := agent.NewBudget(agent.BudgetConfig{Steps: 32, Effects: 32, Signals: 64})
	if err != nil {
		t.Fatal(err)
	}
	var inputCalls int
	childBinding, err := planning.NewChildBinding(planning.ChildBindingConfig{
		Action: delegate, DeploymentRef: childDeployment.DeploymentRef(), Budget: budget,
		Input: func(input agent.Input, observed planning.WorldState) (agent.Input, error) {
			inputCalls++
			if !input.Valid() || observed.Truth("world.done") != planning.Unknown {
				return agent.Input{}, errors.New("unexpected child input source")
			}
			return input, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parentDeployment := newManagedDeployment(t, managedDeploymentConfig{
		name: "planning.parent", goal: mustGoal(t, done),
		bindings: []planning.ActionBinding{childBinding}, sensor: world,
	})
	resolver := managedResolver{childDeployment.DeploymentRef(): childDeployment}
	result := runManaged(t, agent.EngineConfig{DeploymentResolver: resolver}, parentDeployment)
	output := managedOutput(t, result)
	if output.Outcome != planning.OutcomeAchieved || inputCalls != 1 || len(output.Attempts) != 1 ||
		output.Attempts[0].ActionName != "action.delegate" || output.Attempts[0].Status != planning.AttemptSucceeded {
		t.Fatalf("output = %#v, child input calls = %d", output, inputCalls)
	}
}

func TestManagedPlanningValidatesDispatcherBindingsAndCapabilities(t *testing.T) {
	done := mustCondition(t, "world.done", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "action.finish", Description: "Finish the work.", Effects: []planning.Condition{done},
	})
	capability, err := agent.ParseCapability("planning.finish")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := planning.NewDispatcherBinding(planning.DispatcherBindingConfig{
		Action: action, RequiredCapabilities: []agent.Capability{capability},
	})
	if err != nil {
		t.Fatal(err)
	}
	world := newManagedWorld(t)
	definition := newManagedDefinition(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{binding},
	})
	executor := world.apply(action)
	tests := []struct {
		name      string
		sensor    planning.Sensor
		executors map[string]planning.ActionExecutor
	}{
		{name: "missing sensor", executors: map[string]planning.ActionExecutor{"action.finish": executor}},
		{name: "missing executor", sensor: world},
		{name: "extra executor", sensor: world, executors: map[string]planning.ActionExecutor{
			"action.finish": executor, "action.extra": executor,
		}},
		{name: "typed nil sensor", sensor: planning.SensorFunc(nil), executors: map[string]planning.ActionExecutor{"action.finish": executor}},
		{name: "typed nil executor", sensor: world, executors: map[string]planning.ActionExecutor{"action.finish": planning.ActionExecutorFunc(nil)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := planning.NewDispatcher(definition, planning.DispatcherConfig{
				Sensor: test.sensor, ActionExecutors: test.executors,
			}); !errors.Is(err, planning.ErrInvalidDispatcherConfig) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	deployment := newManagedDeployment(t, managedDeploymentConfig{
		goal: mustGoal(t, done), bindings: []planning.ActionBinding{binding}, sensor: world,
		executors: map[string]planning.ActionExecutor{"action.finish": executor},
	})
	result := runManaged(t, agent.EngineConfig{}, deployment)
	assertFailure(t, result, agent.FailureKindContract, "engine.capability.denied")
	if world.truth("world.done") != planning.Unknown {
		t.Fatal("capability-denied Action reached its executor")
	}
}

func TestActionFailureDiagnosticRejectsNonPortableText(t *testing.T) {
	tests := []string{"", " leading", "trailing ", string([]byte{0xff})}
	for _, diagnostic := range tests {
		if _, err := planning.ActionFailed(diagnostic); err == nil {
			t.Fatalf("ActionFailed accepted %q", diagnostic)
		}
	}
}

type managedWorld struct {
	mu           sync.Mutex
	state        planning.WorldState
	observations int
}

func newManagedWorld(t testing.TB, conditions ...planning.Condition) *managedWorld {
	t.Helper()
	state, err := planning.NewWorldState(conditions...)
	if err != nil {
		t.Fatal(err)
	}
	return &managedWorld{state: state}
}

func (m *managedWorld) Sense(_ context.Context, _ planning.SenseRequest) (planning.WorldState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observations++
	return planning.NewWorldState(m.state.Conditions()...)
}

func (m *managedWorld) apply(action planning.Action) planning.ActionExecutor {
	return planning.ActionExecutorFunc(func(_ context.Context, _ planning.ActionRequest) (planning.ActionResult, error) {
		m.mu.Lock()
		next, err := m.state.Apply(action.Effects()...)
		if err == nil {
			m.state = next
		}
		m.mu.Unlock()
		if err != nil {
			return planning.ActionResult{}, err
		}
		return planning.ActionSucceeded(), nil
	})
}

func (m *managedWorld) applyState(t testing.TB, action planning.Action) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := m.state.Apply(action.Effects()...)
	if err != nil {
		t.Fatal(err)
	}
	m.state = next
}

func (m *managedWorld) observationCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.observations
}

func (m *managedWorld) truth(key string) planning.Truth {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Truth(key)
}

type managedDeploymentConfig struct {
	name              string
	goal              planning.Goal
	bindings          []planning.ActionBinding
	sensor            planning.Sensor
	executors         map[string]planning.ActionExecutor
	planner           planning.Planner
	maxActionAttempts uint32
}

func newManagedDefinition(t testing.TB, config managedDeploymentConfig) *planning.Definition {
	t.Helper()
	inputSchema, err := agent.SchemaFor[struct{}]()
	if err != nil {
		t.Fatal(err)
	}
	planner := config.planner
	if planner == nil {
		planner = goap.New(goap.Config{})
	}
	maxActionAttempts := config.maxActionAttempts
	if maxActionAttempts == 0 {
		maxActionAttempts = 8
	}
	name := config.name
	if name == "" {
		name = "planning.test"
	}
	definition, err := planning.NewDefinition(planning.DefinitionConfig{
		Name: name, Description: "Exercise managed goal-directed execution.",
		InputSchema: inputSchema, Goal: config.goal, Actions: config.bindings,
		Planner: planner, MaxActionAttempts: maxActionAttempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func newManagedDeployment(t testing.TB, config managedDeploymentConfig) agent.Deployment {
	t.Helper()
	definition := newManagedDefinition(t, config)
	sensor := config.sensor
	if sensor == nil {
		world := newManagedWorld(t)
		sensor = world
	}
	dispatcher, err := planning.NewDispatcher(definition, planning.DispatcherConfig{
		Sensor: sensor, ActionExecutors: config.executors,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("planning-test-implementation:" + definition.Descriptor().Name())),
		ConfigurationDigest:  agent.ComputeDigest([]byte("planning-test-configuration:" + definition.Descriptor().Name())),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func mustDispatcherBinding(t testing.TB, action planning.Action) planning.ActionBinding {
	t.Helper()
	binding, err := planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: action})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func runManaged(t testing.TB, config agent.EngineConfig, deployment agent.Deployment) agent.Result {
	t.Helper()
	engine, err := agent.NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := engine.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	}()
	input, err := agent.EncodeInput(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func managedOutput(t testing.TB, result agent.Result) planning.Output {
	t.Helper()
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	erased, ok := result.Output()
	if !ok {
		t.Fatal("completed Planning Process has no output")
	}
	output, err := erased.Decode[planning.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if err := output.Validate(); err != nil {
		t.Fatal(err)
	}
	return output
}

func assertFailure(t testing.TB, result agent.Result, kind agent.FailureKind, code string) {
	t.Helper()
	if result.Status() != agent.StatusFailed {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	failure, ok := result.Termination().Failure()
	if !ok || failure.Kind() != kind || failure.Code() != code {
		t.Fatalf("failure = %#v, present = %t", failure, ok)
	}
}

func attemptNames(attempts []planning.Attempt) []string {
	names := make([]string, len(attempts))
	for index, attempt := range attempts {
		names[index] = attempt.ActionName
	}
	return names
}

type managedResolver map[agent.DeploymentRef]agent.Deployment

func (m managedResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, found := m[reference]
	if !found {
		return agent.Deployment{}, errors.New("deployment not found")
	}
	return deployment, nil
}
