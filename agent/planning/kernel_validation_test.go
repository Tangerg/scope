package planning

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/jsonschema"
)

func planningValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

// validationFixture is a Config together with the declarations newDefinition
// receives once Compile has split the executors away from the Actions.
type validationFixture struct {
	Config
	actions []actionDeclaration
}

func (v validationFixture) definition() (*definition, error) {
	return newDefinition(v.Config, planningValue(jsonschema.For[string]()), v.actions)
}

func validationConfig(t *testing.T) validationFixture {
	t.Helper()
	done := mustCondition(t, "world.done", TruthTrue)
	action := mustAction(t, ActionConfig{Name: "action.finish", Description: "Finish ready work.",
		Preconditions: []Condition{mustCondition(t, "world.ready", TruthTrue)}, Effects: []Condition{done}})
	return validationFixture{Config: Config{
		Name: "test.planning.validation", Description: "Validate planning ownership and recovery.",
		Goal:              mustGoal(t, done),
		MaxActionAttempts: run.NewQuota(3),
		Planner: PlannerFunc(func(_ context.Context, problem Problem) (Plan, bool, error) {
			actions := problem.Actions()
			if len(actions) == 0 {
				return Plan{}, false, nil
			}
			plan, err := NewPlan([]PlannedAction{planningValue(NewPlannedAction(actions[0].Name()))})
			return plan, true, err
		}),
	}, actions: []actionDeclaration{{action: action, policy: run.EffectPolicy{Replay: run.ReplayPolicyNever}}}}
}

func validationTurn(step uint64, messages ...run.Message) run.Turn {
	return run.Turn{Process: planningValue(run.ParseProcessID("planning-validation")), Step: step, Messages: messages}
}

func validationWorld(t *testing.T) WorldState {
	t.Helper()
	return mustWorldState(t, mustCondition(t, "world.ready", TruthTrue))
}

func TestPlanningRestorePreservesActionAndProgressOwnership(t *testing.T) {
	config := validationConfig(t)
	definition := planningValue(config.definition())
	newState := func() executionState {
		return executionState{Phase: phaseAwaitingAction, Input: planningValue(run.EncodePayload("task")).JSON(),
			WorldState: validationWorld(t), CurrentActionName: config.actions[0].action.Name(),
			PendingEffect: new(validationTurn(2).EffectID(0))}
	}
	if _, err := definition.Restore(t.Context(), planningValue(newState().snapshot())); err != nil {
		t.Fatalf("valid pending action rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*executionState)
	}{
		{"unknown phase", func(s *executionState) { s.Phase = phase("other") }},
		{"input schema", func(s *executionState) { s.Input = []byte(`42`) }},
		{"unknown action", func(s *executionState) { s.CurrentActionName = "action.unknown" }},
		{"inapplicable action", func(s *executionState) { s.WorldState = WorldState{} }},
		{"excluded current action", func(s *executionState) {
			s.Attempts = []Attempt{{ActionName: run.QualifiedName(s.CurrentActionName), Status: AttemptFailed, Diagnostic: "refused"}}
		}},
		{"attempt unknown action", func(s *executionState) {
			s.Attempts = []Attempt{{ActionName: "action.unknown", Status: AttemptSucceeded}}
		}},
		{"attempt missing diagnostic", func(s *executionState) {
			s.Attempts = []Attempt{{ActionName: run.QualifiedName(s.CurrentActionName), Status: AttemptFailed}}
		}},
		{"attempt after exclusion", func(s *executionState) {
			s.Attempts = []Attempt{{ActionName: run.QualifiedName(s.CurrentActionName), Status: AttemptUnconfirmed}, {ActionName: run.QualifiedName(s.CurrentActionName), Status: AttemptSucceeded}}
		}},
		{"attempt bound includes pending", func(s *executionState) {
			s.Attempts = []Attempt{{ActionName: run.QualifiedName(s.CurrentActionName), Status: AttemptSucceeded}, {ActionName: run.QualifiedName(s.CurrentActionName), Status: AttemptSucceeded}, {ActionName: run.QualifiedName(s.CurrentActionName), Status: AttemptSucceeded}}
		}},
		{"dispatcher retains child", func(s *executionState) {
			s.PendingEffect = nil
			s.Child = &child.Invocation{Process: new(planningValue(validationTurn(1).Process.Child(planningValue(planningChildKey(s.CurrentActionName, 1)))))}
		}},
		{"missing effect", func(s *executionState) { s.PendingEffect = nil }},
		{"missing current action", func(s *executionState) { s.CurrentActionName = "" }},
		{"initial phase repeats attempts", func(s *executionState) {
			s.Attempts = []Attempt{{ActionName: run.QualifiedName(s.CurrentActionName), Status: AttemptSucceeded}}
			s.CurrentActionName, s.PendingEffect, s.Phase = "", nil, phaseReadySense
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newState()
			test.change(&state)
			if _, err := definition.Restore(t.Context(), planningValue(state.snapshot())); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("restored contradictory progress: %v", err)
			}
		})
	}
	if _, err := definition.Start(planningValue(run.EncodePayload(42))); err == nil {
		t.Fatal("planning accepted input outside its declared contract")
	}
}

func TestPlanningChildRestoreRequiresTheExactOutstandingAttempt(t *testing.T) {
	config := validationConfig(t)
	runtime := conformancetest.Runtime(t, run.Config{})
	stage := planningValue(workflow.Transform("identity", func(_ context.Context, value string) (string, error) { return value, nil }))
	program := planningValue(workflow.Compile[string, string](runtime.Compiler(), workflow.Config{Name: "test.planning.child", Description: "Perform one planning action.", Revision: "one", Stages: []workflow.Stage{stage}}))
	config.actions[0] = planningValue(NewChildBinding(ChildBindingConfig{Action: config.actions[0].action, Child: run.ChildBinding{Program: program.Erase()}})).declaration
	definition := planningValue(config.definition())
	key := planningValue(planningChildKey(config.actions[0].action.Name(), 1))
	newState := func() executionState {
		return executionState{Phase: phaseAwaitingAction, Input: planningValue(run.EncodePayload("task")).JSON(), WorldState: validationWorld(t),
			CurrentActionName: config.actions[0].action.Name(), Child: &child.Invocation{Process: new(planningValue(validationTurn(1).Process.Child(key)))}}
	}
	if _, err := definition.Restore(t.Context(), planningValue(newState().snapshot())); err != nil {
		t.Fatalf("valid child ownership rejected: %v", err)
	}
	failed := planningValue(run.NewFailure(run.FailureKindExternal, "test.admission.rejected", "child refused"))
	for _, test := range []struct {
		name   string
		change func(*executionState)
	}{
		{"missing child", func(s *executionState) { s.Child = nil }},
		{"wrong action identity", func(s *executionState) {
			s.Child.Process = new(planningValue(validationTurn(1).Process.Child(planningValue(planningChildKey("action.other", 1)))))
		}},
		{"wrong attempt identity", func(s *executionState) {
			s.Child.Process = new(planningValue(validationTurn(1).Process.Child(planningValue(planningChildKey(s.CurrentActionName, 2)))))
		}},
		{"wrong request ordinal", func(s *executionState) { s.Child = &child.Invocation{Request: new(validationTurn(2).EffectID(1))} }},
		{"already rejected", func(s *executionState) { s.Child = &child.Invocation{Rejection: &failed} }},
		{"wrong phase", func(s *executionState) {
			s.Phase, s.PendingEffect = phaseAwaitingSense, new(validationTurn(2).EffectID(0))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newState()
			test.change(&state)
			if _, err := definition.Restore(t.Context(), planningValue(state.snapshot())); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("restored child ownership outside its declared action: %v", err)
			}
		})
	}
}

func TestPlanningClassifiesPlannerFailureAndCancellation(t *testing.T) {
	cause := errors.New("search exhausted its memory bound")
	for _, test := range []struct {
		name    string
		planner PlannerFunc
		code    string
		kind    run.FailureKind
		wantErr error
	}{
		{"planner failure", func(context.Context, Problem) (Plan, bool, error) { return Plan{}, false, cause }, failureCodePlanningPlannerFailed, run.FailureKindExecution, nil},
		{"empty plan for unmet goal", func(context.Context, Problem) (Plan, bool, error) { return planningValue(NewPlan(nil)), true, nil }, failureCodePlanningPlannerContract, run.FailureKindContract, nil},
		{"action outside domain", func(context.Context, Problem) (Plan, bool, error) {
			return planningValue(NewPlan([]PlannedAction{planningValue(NewPlannedAction("action.unbound"))})), true, nil
		}, failureCodePlanningPlannerContract, run.FailureKindContract, nil},
		{"canceled search", func(context.Context, Problem) (Plan, bool, error) { return Plan{}, false, context.Canceled }, "", run.FailureKindInvalid, context.Canceled},
		{"expired search", func(context.Context, Problem) (Plan, bool, error) { return Plan{}, false, context.DeadlineExceeded }, "", run.FailureKindInvalid, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validationConfig(t)
			config.Planner = test.planner
			definition := planningValue(config.definition())
			execution := planningValue(definition.Start(planningValue(run.EncodePayload("task"))))
			planningValue(execution.Step(t.Context(), validationTurn(1)))
			completion := run.Completion{Effect: validationTurn(1).EffectID(0), Outcome: planningValue(senseSettlement(validationWorld(t), nil))}
			transition, err := execution.Step(t.Context(), validationTurn(2, completion))
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || transition.Valid() {
					t.Fatalf("search cancellation became a durable semantic failure: %+v, %v", transition, err)
				}
				return
			}
			failure, failed := transition.Failure()
			if err != nil || !failed || failure.Code() != test.code || failure.Kind() != test.kind || transition.Consumed() != 1 {
				t.Fatalf("planner failure classification: %+v, %v", transition, err)
			}
		})
	}
}

func TestPlanningSensingFailuresRemainDefiniteAndClassified(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome run.Settlement
		code    string
		kind    run.FailureKind
	}{
		{"sensor error", planningValue(senseSettlement(WorldState{}, errors.New("observation unavailable"))), failureCodePlanningSensingFailed, run.FailureKindExternal},
		{"host rejection", planningValue(planningFailureSettlement(ErrInvalidProtocol)), failureCodePlanningDispatchRejected, run.FailureKindContract},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := planningValue(validationConfig(t).definition())
			execution := planningValue(definition.Start(planningValue(run.EncodePayload("task"))))
			planningValue(execution.Step(t.Context(), validationTurn(1)))
			transition := planningValue(execution.Step(t.Context(), validationTurn(2, run.Completion{Effect: validationTurn(1).EffectID(0), Outcome: test.outcome})))
			failure, failed := transition.Failure()
			if !failed || failure.Code() != test.code || failure.Kind() != test.kind || len(transition.Effects()) != 0 {
				t.Fatalf("sensing failure was retried or misclassified: %+v", transition)
			}
		})
	}
}

func TestPlanningDispatcherRejectsLocalContractErrorsBeforeExternalWork(t *testing.T) {
	config := validationConfig(t)
	executor := ActionExecutorFunc(func(context.Context, ActionRequest) (ActionResult, error) {
		t.Error("invalid request reached executor")
		return ActionSucceeded(), nil
	})
	definition := planningValue(config.definition())
	dispatcher := planningValue(newDispatcher(definition, SensorFunc(func(context.Context, SenseRequest) (WorldState, error) {
		t.Error("invalid request reached sensor")
		return WorldState{}, nil
	}), []ActionExecutor{executor}))
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{"unknown member", []byte(`{"input":"task","other":true}`)},
		{"input schema", mustJSON(t, effectEnvelope{Input: planningValue(run.EncodePayload(42))})},
		{"unknown action", mustJSON(t, effectEnvelope{Input: planningValue(run.EncodePayload("task")), Action: &actionCall{Name: "action.unknown", WorldState: validationWorld(t)}})},
		{"inapplicable action", mustJSON(t, effectEnvelope{Input: planningValue(run.EncodePayload("task")), Action: &actionCall{Name: definition.bindings[0].action.Name()}})},
	} {
		t.Run(test.name, func(t *testing.T) {
			conformancetest.CheckDispatcherRejection(t, dispatcher, planningValue(run.NewDispatcherEffect(test.payload)))
		})
	}
}

func TestPlanningChildInputFailureDeclaresNoSpawn(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	stage := planningValue(workflow.Transform("identity", func(_ context.Context, value string) (string, error) { return value, nil }))
	program := planningValue(workflow.Compile[string, string](runtime.Compiler(), workflow.Config{Name: "test.planning.child-input", Description: "Perform one planning action.", Revision: "one", Stages: []workflow.Stage{stage}}))
	for _, test := range []struct {
		name  string
		input ChildInputFunc
		code  string
	}{
		{"conversion error", func(run.Payload, WorldState) (run.Payload, error) {
			return run.Payload{}, errors.New("conversion refused")
		}, failureCodePlanningChildInputFailed},
		{"invalid output", func(run.Payload, WorldState) (run.Payload, error) { return run.Payload{}, nil }, failureCodePlanningChildInputInvalid},
		{"schema violation", func(run.Payload, WorldState) (run.Payload, error) { return run.EncodePayload(42) }, failureCodePlanningChildInputInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validationConfig(t)
			config.actions[0] = planningValue(NewChildBinding(ChildBindingConfig{Action: config.actions[0].action, Child: run.ChildBinding{Program: program.Erase()}, Input: test.input})).declaration
			definition := planningValue(config.definition())
			execution := planningValue(definition.Start(planningValue(run.EncodePayload("task"))))
			planningValue(execution.Step(t.Context(), validationTurn(1)))
			transition := planningValue(execution.Step(t.Context(), validationTurn(2, run.Completion{Effect: validationTurn(1).EffectID(0), Outcome: planningValue(senseSettlement(validationWorld(t), nil))})))
			failure, failed := transition.Failure()
			if !failed || failure.Code() != test.code || failure.Kind() != run.FailureKindContract || len(transition.Effects()) != 0 {
				t.Fatalf("invalid child input declared a Spawn: %+v", transition)
			}
		})
	}
}

// Without an input function the planning input reaches the child unchanged,
// so the two schemas must agree before any Program is built.
func TestPlanningRejectsChildWhoseSchemaCannotAcceptThePlanningInput(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	stage := planningValue(workflow.Transform("count", func(_ context.Context, value int) (int, error) { return value, nil }))
	program := planningValue(workflow.Compile[int, int](runtime.Compiler(), workflow.Config{Name: "test.planning.int-child", Description: "Accept a number.", Revision: "one", Stages: []workflow.Stage{stage}}))
	config := validationConfig(t)
	config.actions[0] = planningValue(NewChildBinding(ChildBindingConfig{Action: config.actions[0].action, Child: run.ChildBinding{Program: program.Erase()}})).declaration
	if _, err := config.definition(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("newDefinition error = %v, want ErrInvalidConfig", err)
	}
}

func TestPlanningChildRejectionIsObservedBeforeReplanning(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	stage := planningValue(workflow.Transform("identity", func(_ context.Context, value string) (string, error) { return value, nil }))
	program := planningValue(workflow.Compile[string, string](runtime.Compiler(), workflow.Config{Name: "test.planning.rejected-child", Description: "Perform one planning action.", Revision: "one", Stages: []workflow.Stage{stage}}))
	config := validationConfig(t)
	config.actions[0] = planningValue(NewChildBinding(ChildBindingConfig{Action: config.actions[0].action, Child: run.ChildBinding{Program: program.Erase()}})).declaration
	definition := planningValue(config.definition())
	execution := planningValue(definition.Start(planningValue(run.EncodePayload("task"))))
	planningValue(execution.Step(t.Context(), validationTurn(1)))
	spawn := planningValue(execution.Step(t.Context(), validationTurn(2, run.Completion{Effect: validationTurn(1).EffectID(0), Outcome: planningValue(senseSettlement(validationWorld(t), nil))})))
	if _, ok := spawn.Effects()[0].ChildStart(); !ok {
		t.Fatal("planning did not start a child")
	}
	failure := planningValue(run.NewFailure(run.FailureKindContract, "test.child.rejected", "admission refused"))
	rejected := run.Completion{Effect: validationTurn(2).EffectID(0), Outcome: planningValue(run.NewFailedSettlement(failure, run.Payload{}))}
	observe := planningValue(execution.Step(t.Context(), validationTurn(3, rejected)))
	envelope := planningValue(decodeEffect(observe.Effects()[0].Payload().JSON()))
	if envelope.operation() != operationSense {
		t.Fatal("child rejection replanned against an old world observation")
	}
	restored := planningValue(definition.Restore(t.Context(), planningValue(execution.Snapshot())))
	complete := planningValue(restored.Step(t.Context(), validationTurn(4, run.Completion{Effect: validationTurn(3).EffectID(0), Outcome: planningValue(senseSettlement(validationWorld(t), nil))})))
	payload, completed := complete.Output()
	if !completed {
		t.Fatal("excluded child action was selected again")
	}
	output := planningValue(payload.Decode[Output]())
	if output.Outcome != OutcomeStuck || len(output.Attempts) != 1 || output.Attempts[0].Status != AttemptFailed || output.Attempts[0].Diagnostic != failure.Code()+": "+failure.Message() {
		t.Fatalf("rejection lost its attempt fact: %+v", output)
	}
}

func TestPlanningDefinitionRejectsUnboundOrAmbiguousActions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*validationFixture)
	}{
		{"finite zero attempts", func(c *validationFixture) { c.MaxActionAttempts = run.NewQuota(0) }},
		{"missing planner", func(c *validationFixture) { c.Planner = PlannerFunc(nil) }},
		{"invalid action", func(c *validationFixture) { c.actions = []actionDeclaration{{}} }},
		{"duplicate action", func(c *validationFixture) { c.actions = append(c.actions, c.actions[0]) }},
		{"invalid name", func(c *validationFixture) { c.Name = "invalid name" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validationConfig(t)
			test.change(&config)
			if _, err := config.definition(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("invalid planning declaration accepted: %v", err)
			}
		})
	}
}

func TestPlanningDomainDeclarationsRejectInvalidAndRepeatedFacts(t *testing.T) {
	done := mustCondition(t, "world.done", TruthTrue)
	for _, test := range []struct {
		name   string
		change func(*ActionConfig)
	}{
		{"name", func(c *ActionConfig) { c.Name = "invalid name" }},
		{"description", func(c *ActionConfig) { c.Description = " untrimmed " }},
		{"invalid precondition", func(c *ActionConfig) { c.Preconditions = []Condition{{}} }},
		{"invalid effect", func(c *ActionConfig) { c.Effects = []Condition{{}} }},
		{"duplicate requirement", func(c *ActionConfig) { c.Preconditions = []Condition{done, done} }},
		{"duplicate prediction", func(c *ActionConfig) { c.Effects = []Condition{done, done} }},
		{"missing prediction", func(c *ActionConfig) { c.Effects = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := ActionConfig{Name: "action.finish", Description: "Finish one action.", Effects: []Condition{done}}
			test.change(&config)
			if action, err := NewAction(config); action.Valid() || !errors.Is(err, ErrInvalidAction) {
				t.Fatalf("invalid predictive action accepted: %+v %v", action, err)
			}
		})
	}
	for _, config := range []GoalConfig{
		{Name: "invalid name", Description: "Reach the goal.", Conditions: []Condition{done}},
		{Name: "goal.done", Description: "", Conditions: []Condition{done}},
		{Name: "goal.done", Description: "Reach the goal.", Conditions: []Condition{{}}},
		{Name: "goal.done", Description: "Reach the goal.", Conditions: []Condition{done, done}},
	} {
		if goal, err := NewGoal(config); goal.Valid() || !errors.Is(err, ErrInvalidGoal) {
			t.Fatalf("invalid goal accepted: %+v %v", goal, err)
		}
	}
	goal := mustGoal(t, done)
	action := mustAction(t, ActionConfig{Name: "action.finish", Description: "Finish one action.", Effects: []Condition{done}})
	for _, test := range []struct {
		goal    Goal
		actions []Action
	}{
		{Goal{}, []Action{action}}, {goal, []Action{{}}}, {goal, []Action{action, action}},
	} {
		if problem, err := NewProblem(WorldState{}, test.goal, test.actions...); problem.Valid() || !errors.Is(err, ErrInvalidProblem) {
			t.Fatalf("invalid search domain accepted: %+v %v", problem, err)
		}
	}
	if _, err := (Action{}).Cost(WorldState{}); !errors.Is(err, ErrInvalidActionCost) {
		t.Fatalf("invalid action supplied a search cost: %v", err)
	}
	if _, err := (Action{}).Apply(WorldState{}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("invalid action changed the prediction: %v", err)
	}
	if _, err := NewWorldState(done, done); !errors.Is(err, ErrInvalidWorldState) {
		t.Fatalf("duplicate observations created two fact owners: %v", err)
	}
}

func TestPlanningDecodersPreserveThePreviousValueOnInvalidFacts(t *testing.T) {
	condition := mustCondition(t, "world.ready", TruthTrue)
	if _, err := (Condition{}).MarshalJSON(); !errors.Is(err, ErrInvalidCondition) {
		t.Fatalf("invalid condition serialized: %v", err)
	}
	if err := jsonv2.Unmarshal([]byte(`{"key":"","truth":"true"}`), &condition); !errors.Is(err, ErrInvalidCondition) || condition.Key() != "world.ready" {
		t.Fatalf("invalid identity partially overwrote a condition: %+v %v", condition, err)
	}
	world := validationWorld(t)
	before := world.Key()
	duplicate := []byte(`{"conditions":[{"key":"world.done","truth":"true"},{"key":"world.done","truth":"false"}]}`)
	if err := jsonv2.Unmarshal(duplicate, &world); !errors.Is(err, ErrInvalidWorldState) || world.Key() != before {
		t.Fatalf("contradictory observation overwrote the complete state: %s %v", world.Key(), err)
	}
}

func TestPlanningBindingsRequireOneExecutionOwner(t *testing.T) {
	config := validationConfig(t)
	action := config.actions[0].action
	executor := ActionExecutorFunc(func(context.Context, ActionRequest) (ActionResult, error) { return ActionSucceeded(), nil })
	for _, invalid := range []DispatcherBindingConfig{
		{}, {Action: action}, {Action: action, Executor: ActionExecutorFunc(nil)},
		{Action: action, Executor: executor, Replay: run.ReplayPolicy("invalid")},
		{Action: action, Executor: executor, RequiredCapabilities: []run.Capability{{}}},
	} {
		if _, err := NewDispatcherBinding(invalid); !errors.Is(err, ErrInvalidAction) {
			t.Fatalf("invalid dispatcher binding was admitted: %v", err)
		}
	}
	if _, err := NewChildBinding(ChildBindingConfig{Action: action}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("missing child program was bound: %v", err)
	}
	definition := planningValue(config.definition())
	if _, err := newDispatcher(definition, nil, []ActionExecutor{executor}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing sensor was admitted: %v", err)
	}
	for _, replay := range []run.ReplayPolicy{run.ReplayPolicyNever, run.ReplayPolicySameIdentity} {
		binding := planningValue(NewDispatcherBinding(DispatcherBindingConfig{Action: action, Executor: executor, Replay: replay}))
		config.actions[0] = binding.declaration
		dispatcher := planningValue(newDispatcher(planningValue(config.definition()),
			SensorFunc(func(context.Context, SenseRequest) (WorldState, error) { return validationWorld(t), nil }),
			[]ActionExecutor{executor}))
		effect := planningValue(newActionEffect(planningValue(run.EncodePayload("task")), binding.declaration, validationWorld(t)))
		if dispatcher.Policy(effect).Replay != replay {
			t.Fatal("dispatcher did not preserve the action's replay declaration")
		}
	}
}

func TestPlanningCompileRejectsMissingExecutionAuthorityAndInvalidDomain(t *testing.T) {
	config := validationConfig(t)
	binding := planningValue(NewDispatcherBinding(DispatcherBindingConfig{Action: config.actions[0].action, Executor: ActionExecutorFunc(func(context.Context, ActionRequest) (ActionResult, error) { return ActionSucceeded(), nil })}))
	base := Config{Name: config.Name, Description: config.Description, Goal: config.Goal, Actions: []ActionBinding{binding}, Planner: config.Planner, Revision: "one"}
	compiler := conformancetest.Compiler(t)
	if _, err := Compile[string](compiler, base); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("compilation obscured missing execution authority: %v", err)
	}
	base.Goal = Goal{}
	if _, err := Compile[string](compiler, base); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("compilation obscured invalid domain: %v", err)
	}
	base.Goal = config.Goal
	base.Sensor = SensorFunc(func(context.Context, SenseRequest) (WorldState, error) { return validationWorld(t), nil })
	if _, err := Compile[chan int](compiler, base); err == nil {
		t.Fatal("compilation accepted an input with no portable schema")
	}
}

func TestPlanningInvalidExecutorResultRemainsUnknownUntilResolved(t *testing.T) {
	config := validationConfig(t)
	binding := planningValue(NewDispatcherBinding(DispatcherBindingConfig{Action: config.actions[0].action, Executor: ActionExecutorFunc(func(context.Context, ActionRequest) (ActionResult, error) {
		return ActionResult{}, nil
	})}))
	runtime := conformancetest.Runtime(t, run.Config{})
	program := planningValue(Compile[string](runtime.Compiler(), Config{
		Name: config.Name, Description: config.Description, Goal: config.Goal, Actions: []ActionBinding{binding}, Planner: config.Planner, Revision: "one",
		Sensor: SensorFunc(func(context.Context, SenseRequest) (WorldState, error) { return validationWorld(t), nil }),
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	handle := planningValue(runtime.Start(ctx, program, run.StartRequest[string]{ID: run.StartID{Scope: "planning-validation", Key: "invalid-action-result"}, Input: "task"}))
	var unknown run.EffectID
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if settled, ok := entry.Record.(run.EffectSettled); ok && settled.Outcome.Status() == run.SettlementStatusUnknown {
			unknown = settled.ID
			break
		}
	}
	if !unknown.Valid() {
		t.Fatal("invalid external result became a definite action outcome")
	}
	if err := handle.ReplayUnknownEffect(ctx, unknown); !errors.Is(err, run.ErrEffectReplayForbidden) {
		t.Fatalf("invalid action outcome was implicitly replayable: %v", err)
	}
	if err := handle.Cancel(ctx, "stop after reconciliation"); err != nil {
		t.Fatal(err)
	}
	settlement := planningValue(NewActionSettlement(planningValue(ActionFailed("operator confirmed failure"))))
	if err := handle.ResolveUnknownEffect(ctx, unknown, settlement); err != nil {
		t.Fatal(err)
	}
	if result := planningValue(handle.Await(ctx)); result.Termination().Status() != run.StatusCanceled {
		t.Fatalf("reconciliation replaced the terminal intent: %s", result.Termination().Status())
	}
}

func TestPlanningFailureDiagnosticsRequireAnExplicitDefiniteResult(t *testing.T) {
	for _, diagnostic := range []string{"", " untrimmed ", string([]byte{0xff}), strings.Repeat("x", run.MaxDiagnosticBytes+1)} {
		if result, err := ActionFailed(diagnostic); err == nil || result.Valid() {
			t.Fatalf("invalid diagnostic manufactured a definite result: %+v %v", result, err)
		}
	}
	if _, err := NewActionSettlement(ActionResult{}); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("unspecified result manufactured a settlement: %v", err)
	}
}
