package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/collaboration"
	"github.com/Tangerg/scope/agent/strategy/coordination"
	"github.com/Tangerg/scope/agent/strategy/planning"
	"github.com/Tangerg/scope/agent/strategy/planning/goap"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

func safetyValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func safetyBinding(definition agent.Definition, dispatcher agent.Dispatcher) agent.Deployment {
	return safetyValue(agent.NewDeployment(agent.DeploymentConfig{Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("subtree-safety")), ConfigurationDigest: agent.ComputeDigest([]byte(definition.Descriptor().Name()))}))
}

type safetyResolver map[agent.DeploymentRef]agent.Deployment

func (s safetyResolver) Resolve(ref agent.DeploymentRef) (agent.Deployment, error) {
	if deployment, ok := s[ref]; ok {
		return deployment, nil
	}
	return agent.Deployment{}, errors.New("unknown safety deployment")
}

// The adapter changes only the competition's input/output contracts. FirstSuccess
// still owns admission and result-boundary completion, including its losing tree.
type safetyCompetition struct {
	descriptor  agent.Descriptor
	competition *coordination.FirstSuccess
	candidates  agent.Payload
	output      agent.Payload
}

func (s *safetyCompetition) Descriptor() agent.Descriptor { return s.descriptor }
func (s *safetyCompetition) Start(input agent.Payload) (agent.Execution, error) {
	if err := s.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	execution, err := s.competition.Start(s.candidates)
	if err != nil {
		return nil, err
	}
	return &safetyCompetitionExecution{Execution: execution, output: s.output}, nil
}
func (s *safetyCompetition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	execution, err := s.competition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &safetyCompetitionExecution{Execution: execution, output: s.output}, nil
}

type safetyCompetitionExecution struct {
	agent.Execution
	output agent.Payload
}

func (s *safetyCompetitionExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	transition, err := s.Execution.Step(ctx, signals)
	if err == nil && transition.Kind() == agent.TransitionKindComplete {
		return agent.Complete(transition.ConsumedSignals(), s.output)
	}
	return transition, err
}

type safetyTimer struct {
	entered chan struct{}
	once    sync.Once
}

func (s *safetyTimer) ReplayPolicy(agent.Effect) agent.ReplayPolicy { return agent.ReplayPolicyNever }
func (s *safetyTimer) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	key, _ := request.Relation().ChildKey()
	if key.String() == "loser" {
		s.once.Do(func() { close(s.entered) })
		<-ctx.Done()
		return agent.Settlement{}, ctx.Err()
	}
	select {
	case <-s.entered:
	case <-ctx.Done():
		return agent.Settlement{}, ctx.Err()
	}
	return (coordination.Timer{}).Dispatch(ctx, request, emit)
}

func safetyChild[I, O any](output O) (agent.Deployment, safetyResolver) {
	timer := safetyBinding(safetyValue(coordination.NewDeadline(coordination.DeadlineConfig{Name: "safety.timer", Description: "A controlled competition candidate."})), &safetyTimer{entered: make(chan struct{})})
	budget := agent.Budget{Steps: agent.NewQuota(16), Effects: agent.NewQuota(16), Signals: agent.NewQuota(16)}
	candidates := []agent.ChildSpec{}
	for _, key := range []string{"winner", "loser"} {
		candidates = append(candidates, agent.ChildSpec{Key: safetyValue(agent.ParseChildKey(key)), DeploymentRef: timer.DeploymentRef(), Input: safetyValue(agent.EncodePayload(time.Unix(1, 0).UTC())), Budget: budget})
	}
	definition := &safetyCompetition{descriptor: safetyValue(agent.NewDescriptor(agent.DescriptorConfig{Name: "safety.competition", Description: "Complete while a loser retains uncertainty.", InputSchema: safetyValue(agent.SchemaFor[I]()), OutputSchema: safetyValue(agent.SchemaFor[O]())})),
		competition: safetyValue(coordination.NewFirstSuccess(coordination.FirstSuccessConfig{Name: "safety.first_success", Description: "Select a winner.", MaxCandidates: 2, Accept: func(context.Context, agent.ChildOutcome) (bool, error) { return true, nil }})), candidates: safetyValue(agent.EncodePayload(candidates)), output: safetyValue(agent.EncodePayload(output))}
	child := safetyBinding(definition, nil)
	return child, safetyResolver{child.DeploymentRef(): child, timer.DeploymentRef(): timer}
}

func assertSafetyFailure(t *testing.T, root agent.Deployment, resolver safetyResolver, input agent.Payload, code string, assertStopped func()) agent.ExecutionState {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := agent.NewMemoryTreeCommitter()
	config := agent.EngineConfig{DeploymentResolver: resolver, TreeCommitter: store}
	engine := safetyValue(agent.NewEngine(config))
	defer func() {
		if err := engine.Close(context.WithoutCancel(ctx)); err != nil {
			t.Error(err)
		}
	}()
	result := safetyValue(engine.Run(ctx, root, input))
	assertStopped()
	assertFailure := func(result agent.Result) {
		t.Helper()
		failure, ok := result.Termination().Failure()
		if result.Status() != agent.StatusFailed || !ok || failure.Kind() != agent.FailureKindExternal || failure.Code() != code {
			t.Fatalf("result=%s failure=%+v; want failed / external / %s", result.Status(), failure, code)
		}
		if output, present := result.Output(); present {
			t.Fatalf("unsafe child produced parent output: %s", output.JSON())
		}
	}
	assertFailure(result)
	tree, ok, err := store.LoadTree(ctx, result.ProcessID())
	if err != nil || !ok {
		t.Fatalf("load tree=%t %v", ok, err)
	}
	state := assertSafetyTree(t, tree, result.ProcessID())
	restoredEngine := safetyValue(agent.NewEngine(config))
	defer func() {
		if closeErr := restoredEngine.Close(context.WithoutCancel(ctx)); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	restored := safetyValue(restoredEngine.RestoreTree(ctx, root, tree))
	recovered := safetyValue(restored.Await(ctx))
	assertStopped()
	assertFailure(recovered)
	restoredTree, ok, err := store.LoadTree(ctx, result.ProcessID())
	if err != nil || !ok {
		t.Fatalf("load restored tree=%t %v", ok, err)
	}
	restoredState := assertSafetyTree(t, restoredTree, result.ProcessID())
	if state.Kind() != restoredState.Kind() || !bytes.Equal(state.Payload(), restoredState.Payload()) {
		t.Fatalf("failed Strategy advanced on restore: before=%s after=%s", state.Payload(), restoredState.Payload())
	}
	return state
}

func assertSafetyTree(t *testing.T, tree agent.TreeSnapshot, rootID agent.ProcessID) agent.ExecutionState {
	t.Helper()
	var state agent.ExecutionState
	processes := tree.ProcessSnapshots()
	competitions, winners, losers := 0, 0, 0
	for _, process := range processes {
		if process.ProcessID() == rootID {
			state = process.CommittedExecutionState()
		}
		if process.DeploymentRef().Name() == "safety.competition" && process.Status() == agent.StatusCompleted {
			competitions++
		}
		key, _ := process.Relation().ChildKey()
		if process.DeploymentRef().Name() != "safety.timer" {
			continue
		}
		switch key.String() {
		case "winner":
			if process.Status() == agent.StatusCompleted && len(process.UnknownEffectIDs()) == 0 {
				winners++
			}
		case "loser":
			if len(process.UnknownEffectIDs()) == 1 && process.Usage().PreparedEffects == 1 {
				losers++
			}
		}
	}
	if len(processes) != 4 || competitions != 1 || winners != 1 || losers != 1 {
		t.Fatalf("competition witness: processes=%d completed competitions=%d winners=%d unresolved losers=%d", len(processes), competitions, winners, losers)
	}
	return state
}

func TestWorkflowRejectsUnresolvedFirstSuccessSubtrees(t *testing.T) {
	for _, test := range []struct {
		kind string
		code string
	}{
		{kind: "call", code: "workflow.call.unresolved_effects"},
		{kind: "switch", code: "workflow.switch.unresolved_effects"},
		{kind: "loop", code: "workflow.loop.unresolved_effects"},
		{kind: "fork", code: "workflow.fork.branch_unresolved_effects"},
		{kind: "map", code: "workflow.map.item_unresolved_effects"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			child, resolver := safetyChild[string]("winner")
			budget := agent.Budget{Steps: agent.NewQuota(128), Effects: agent.NewQuota(128), Signals: agent.NewQuota(128)}
			var stage, after workflow.Stage
			var afterCalls, predicateCalls, reducerCalls atomic.Int32
			input := safetyValue(agent.EncodePayload("work"))
			failAfter := func(context.Context, string) (string, error) {
				afterCalls.Add(1)
				return "", errors.New("next stage ran")
			}
			after = safetyValue(workflow.Transform("after", failAfter))
			switch test.kind {
			case "call":
				stage = safetyValue(workflow.Call(workflow.CallConfig{ID: test.kind, Deployment: child, Budget: budget}))
			case "switch":
				stage = safetyValue(workflow.Switch(workflow.SwitchConfig[string]{ID: test.kind, Select: func(context.Context, string) (string, error) { return "chosen", nil }, Cases: []workflow.SwitchCase{{ID: "chosen", Deployment: child, Budget: budget}}}))
			case "loop":
				stage = safetyValue(workflow.Loop(workflow.LoopConfig[string]{ID: test.kind, Body: child, Budget: budget, MaxIterations: agent.NewQuota(2), Predicate: func(context.Context, string) (bool, error) {
					predicateCalls.Add(1)
					return false, nil
				}}))
				after = safetyValue(workflow.Transform("after", func(context.Context, workflow.LoopResult[string]) (string, error) {
					afterCalls.Add(1)
					return "", errors.New("next stage ran")
				}))
			case "fork":
				stage = safetyValue(workflow.Fork(workflow.ForkConfig[string, string, string]{ID: test.kind, WindowSize: 1, Branches: []workflow.ForkBranch{{ID: "first", Deployment: child, Budget: budget}, {ID: "second", Deployment: child, Budget: budget}}, Reduce: func(context.Context, []string) (string, error) {
					reducerCalls.Add(1)
					return "", errors.New("fanout reduced unsafe output")
				}}))
			case "map":
				stage = safetyValue(workflow.Map(workflow.MapConfig[string, string]{ID: test.kind, Deployment: child, Budget: budget, WindowSize: 1, MaxItems: 2}))
				input = safetyValue(agent.EncodePayload([]string{"one", "two"}))
				after = safetyValue(workflow.Transform("after", func(context.Context, []string) (string, error) {
					afterCalls.Add(1)
					return "", errors.New("next stage ran")
				}))
			}
			root := safetyBinding(safetyValue(workflow.NewDefinition(workflow.DefinitionConfig{Name: "safety.workflow", Description: "Reject unsafe outputs.", Stages: []workflow.Stage{stage, after}})), nil)
			assertSafetyFailure(t, root, resolver, input, test.code, func() {
				if afterCalls.Load() != 0 || predicateCalls.Load() != 0 || reducerCalls.Load() != 0 {
					t.Errorf("unsafe output consumed: after=%d predicate=%d reducer=%d", afterCalls.Load(), predicateCalls.Load(), reducerCalls.Load())
				}
			})
		})
	}
}

func TestCollaborationRejectsUnresolvedCoordinatorDecision(t *testing.T) {
	for _, mode := range []collaboration.Mode{collaboration.Continue, collaboration.Wait, collaboration.Complete} {
		t.Run(string(mode), func(t *testing.T) {
			output := safetyValue(agent.EncodePayload("done"))
			decision := collaboration.Decision{Mode: mode, State: safetyValue(agent.EncodePayload("initial"))}
			worker := safetyBinding(safetyValue(coordination.NewInputGate(coordination.InputGateConfig{Name: "safety.worker", Description: "Never admitted.", RequestSchema: safetyValue(agent.SchemaFor[string]()), AnswerSchema: safetyValue(agent.SchemaFor[string]())})), nil)
			if mode == collaboration.Complete {
				decision.Output = output
			} else {
				decision.Tasks = []collaboration.TaskRequest{{Key: safetyValue(agent.ParseChildKey("new-work")), Worker: "safety.worker", Input: safetyValue(agent.EncodePayload("work"))}}
			}
			child, resolver := safetyChild[collaboration.Turn](decision)
			resolver[worker.DeploymentRef()] = worker
			definition := safetyValue(collaboration.NewDefinition(collaboration.DefinitionConfig{Name: "safety.collaboration", Description: "Reject unsafe coordinator decisions.", Coordinator: collaboration.WorkerConfig{Deployment: child, Budget: agent.Budget{Steps: agent.NewQuota(128), Effects: agent.NewQuota(128), Signals: agent.NewQuota(128)}}, Workers: []collaboration.WorkerConfig{{Deployment: worker, Budget: agent.Budget{Steps: agent.NewQuota(16), Effects: agent.NewQuota(16), Signals: agent.NewQuota(16)}}}, StateSchema: safetyValue(agent.SchemaFor[string]()), OutputSchema: safetyValue(agent.SchemaFor[string]()), MaxTurns: agent.NewQuota(2), MaxTasks: agent.NewQuota(2), MaxConcurrentTasks: 2, MaxControlsPerTurn: 2}))
			state := assertSafetyFailure(t, safetyBinding(definition, nil), resolver, safetyValue(agent.EncodePayload("initial")), "collaboration.coordinator.unresolved_effects", func() {})
			var wire map[string]json.RawMessage
			if err := jsonv2.Unmarshal(state.Payload(), &wire); err != nil {
				t.Fatal(err)
			}
			if string(wire["number"]) != "1" || len(wire["tasks"]) != 0 || len(wire["controls"]) != 0 {
				t.Fatalf("unsafe decision adopted: %s", state.Payload())
			}
			wire["mode"] = safetyValue(jsonv2.Marshal(mode))
			wire["phase"] = json.RawMessage(`"completed"`)
			wire["output"] = output.JSON()
			forged := safetyValue(agent.ParseExecutionState(state.Kind(), safetyValue(jsonv2.Marshal(wire))))
			if _, err := definition.Restore(t.Context(), forged); !errors.Is(err, collaboration.ErrInvalidExecutionState) {
				t.Fatal(fmt.Errorf("unsafe applied decision restored: %w", err))
			}
		})
	}
}

func TestPlanningRejectsUnresolvedChildAction(t *testing.T) {
	for _, next := range []string{"goal achieved", "next action"} {
		t.Run(next, func(t *testing.T) {
			child, resolver := safetyChild[string]("winner")
			ready := safetyValue(planning.NewCondition("world.ready", planning.True))
			done := safetyValue(planning.NewCondition("world.done", planning.True))
			goal := safetyValue(planning.NewGoal(planning.GoalConfig{
				Name: "safety.goal", Description: "Finish the work.", Conditions: []planning.Condition{done},
			}))
			delegate := safetyValue(planning.NewAction(planning.ActionConfig{
				Name: "action.delegate", Description: "Delegate preparation.", Effects: []planning.Condition{ready},
			}))
			finish := safetyValue(planning.NewAction(planning.ActionConfig{
				Name: "action.finish", Description: "Finish after preparation.",
				Preconditions: []planning.Condition{ready}, Effects: []planning.Condition{done},
			}))
			var senseCalls, planCalls, inputCalls, actionCalls atomic.Int32
			binding := safetyValue(planning.NewChildBinding(planning.ChildBindingConfig{
				Action: delegate, DeploymentRef: child.DeploymentRef(),
				Budget: agent.Budget{Steps: agent.NewQuota(128), Effects: agent.NewQuota(128), Signals: agent.NewQuota(128)},
				Input: func(input agent.Payload, _ planning.WorldState) (agent.Payload, error) {
					inputCalls.Add(1)
					return input, nil
				},
			}))
			planner := goap.New(goap.Config{})
			definition := safetyValue(planning.NewDefinition(planning.DefinitionConfig{
				Name: "safety.planning", Description: "Reject an uncertain child before reobserving the world.",
				InputSchema: safetyValue(agent.SchemaFor[string]()), Goal: goal,
				Actions: []planning.ActionBinding{binding, safetyValue(planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: finish}))},
				Planner: planning.PlannerFunc(func(ctx context.Context, problem planning.Problem) (planning.Plan, bool, error) {
					planCalls.Add(1)
					return planner.Plan(ctx, problem)
				}),
				MaxActionAttempts: agent.NewQuota(2),
			}))
			dispatcher := safetyValue(planning.NewDispatcher(definition, planning.DispatcherConfig{
				Sensor: planning.SensorFunc(func(context.Context, planning.SenseRequest) (planning.WorldState, error) {
					switch senseCalls.Add(1) {
					case 1:
						return planning.WorldState{}, nil
					case 2:
						if next == "next action" {
							return planning.NewWorldState(ready)
						}
					}
					return planning.NewWorldState(ready, done)
				}),
				ActionExecutors: map[string]planning.ActionExecutor{
					"action.finish": planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
						actionCalls.Add(1)
						return planning.ActionSucceeded(), nil
					}),
				},
			}))
			assertSafetyFailure(t, safetyBinding(definition, dispatcher), resolver, safetyValue(agent.EncodePayload("work")), "planning.child.unresolved_effects", func() {
				if senseCalls.Load() != 1 || planCalls.Load() != 1 || inputCalls.Load() != 1 || actionCalls.Load() != 0 {
					t.Errorf("planning advanced past uncertainty: sense=%d plan=%d input=%d action=%d", senseCalls.Load(), planCalls.Load(), inputCalls.Load(), actionCalls.Load())
				}
			})
		})
	}
}
