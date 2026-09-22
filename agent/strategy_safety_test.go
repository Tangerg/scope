package agent_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/collaboration"
	"github.com/Tangerg/scope/agent/strategy/coordination"
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

func assertSafetyFailure(t *testing.T, root agent.Deployment, resolver safetyResolver, input agent.Payload, code string) agent.ExecutionState {
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
	failure, ok := result.Termination().Failure()
	if !ok || !strings.HasSuffix(failure.Code(), code) {
		t.Fatalf("result=%s failure=%+v", result.Status(), failure)
	}
	tree, ok, err := store.LoadTree(ctx, result.ProcessID())
	if err != nil || !ok {
		t.Fatalf("load tree=%t %v", ok, err)
	}
	var state agent.ExecutionState
	unresolved, completed := 0, 0
	for _, process := range tree.ProcessSnapshots() {
		if process.ProcessID() == result.ProcessID() {
			state = process.CommittedExecutionState()
		}
		unresolved += len(process.UnknownEffectIDs())
		if process.DeploymentRef().Name() == "safety.competition" && process.Status() == agent.StatusCompleted {
			completed++
		}
	}
	if unresolved != 1 || completed != 1 {
		t.Fatalf("competition witness: completed=%d unresolved=%d", completed, unresolved)
	}
	restoredEngine := safetyValue(agent.NewEngine(config))
	defer func() {
		if err := restoredEngine.Close(context.WithoutCancel(ctx)); err != nil {
			t.Error(err)
		}
	}()
	restored := safetyValue(restoredEngine.RestoreTree(ctx, root, tree))
	recovered := safetyValue(restored.Await(ctx))
	restoredFailure, ok := recovered.Termination().Failure()
	if !ok || restoredFailure.Code() != failure.Code() {
		t.Fatalf("recovered failure=%+v", restoredFailure)
	}
	return state
}

func TestWorkflowRejectsUnresolvedFirstSuccessSubtrees(t *testing.T) {
	for _, kind := range []string{"call", "switch", "loop", "fork", "map"} {
		t.Run(kind, func(t *testing.T) {
			child, resolver := safetyChild[string]("winner")
			budget := agent.Budget{Steps: agent.NewQuota(128), Effects: agent.NewQuota(128), Signals: agent.NewQuota(128)}
			var stage, after workflow.Stage
			input := safetyValue(agent.EncodePayload("work"))
			failAfter := func(context.Context, string) (string, error) { return "", errors.New("next stage ran") }
			after = safetyValue(workflow.Transform("after", failAfter))
			switch kind {
			case "call":
				stage = safetyValue(workflow.Call(workflow.CallConfig{ID: kind, Deployment: child, Budget: budget}))
			case "switch":
				stage = safetyValue(workflow.Switch(workflow.SwitchConfig[string]{ID: kind, Select: func(context.Context, string) (string, error) { return "chosen", nil }, Cases: []workflow.SwitchCase{{ID: "chosen", Deployment: child, Budget: budget}}}))
			case "loop":
				stage = safetyValue(workflow.Loop(workflow.LoopConfig[string]{ID: kind, Body: child, Budget: budget, MaxIterations: agent.NewQuota(2), Predicate: func(context.Context, string) (bool, error) { t.Error("loop adopted unsafe output"); return false, nil }}))
				after = safetyValue(workflow.Transform("after", func(context.Context, workflow.LoopResult[string]) (string, error) {
					return "", errors.New("next stage ran")
				}))
			case "fork":
				stage = safetyValue(workflow.Fork(workflow.ForkConfig[string, string, string]{ID: kind, WindowSize: 1, Branches: []workflow.ForkBranch{{ID: "first", Deployment: child, Budget: budget}, {ID: "second", Deployment: child, Budget: budget}}, Reduce: func(context.Context, []string) (string, error) { return "", errors.New("fanout reduced unsafe output") }}))
			case "map":
				stage = safetyValue(workflow.Map(workflow.MapConfig[string, string]{ID: kind, Deployment: child, Budget: budget, WindowSize: 1, MaxItems: 2}))
				input = safetyValue(agent.EncodePayload([]string{"one", "two"}))
				after = safetyValue(workflow.Transform("after", func(context.Context, []string) (string, error) { return "", errors.New("next stage ran") }))
			}
			root := safetyBinding(safetyValue(workflow.NewDefinition(workflow.DefinitionConfig{Name: "safety.workflow", Description: "Reject unsafe outputs.", Stages: []workflow.Stage{stage, after}})), nil)
			assertSafetyFailure(t, root, resolver, input, "unresolved_effects")
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
			state := assertSafetyFailure(t, safetyBinding(definition, nil), resolver, safetyValue(agent.EncodePayload("initial")), "coordinator.unresolved_effects")
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
			forged := safetyValue(agent.NewExecutionState(state.Kind(), safetyValue(jsonv2.Marshal(wire))))
			if _, err := definition.Restore(t.Context(), forged); !errors.Is(err, collaboration.ErrInvalidState) {
				t.Fatal(fmt.Errorf("unsafe applied decision restored: %w", err))
			}
		})
	}
}
