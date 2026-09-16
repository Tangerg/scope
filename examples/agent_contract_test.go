package examples_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/coordination"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/planning"
	"github.com/Tangerg/scope/agent/strategy/planning/goap"
	"github.com/Tangerg/scope/agent/strategy/workflow"
	"github.com/Tangerg/scope/core/chat"
)

func contractValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func contractDeployment(definition agent.Definition, dispatcher agent.Dispatcher) agent.Deployment {
	return contractValue(agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("contract-test-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(definition.Descriptor().Name())),
	}))
}

type contractResolver map[agent.DeploymentRef]agent.Deployment

func (c contractResolver) Resolve(ref agent.DeploymentRef) (agent.Deployment, error) {
	if deployment, ok := c[ref]; ok {
		return deployment, nil
	}
	return agent.Deployment{}, fmt.Errorf("unresolved test deployment: %v", ref)
}

func contractInteraction(config interaction.DefinitionConfig, model chat.Model) agent.Deployment {
	definition := contractValue(interaction.NewDefinition(config))
	return contractDeployment(definition, contractValue(interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: model})))
}

func contractText(text string) *chat.Response {
	return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart(text))), FinishReason: chat.FinishReasonStop}}
}

func contractToolCall(name, arguments string) *chat.Response {
	return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: name, Arguments: arguments}))), FinishReason: chat.FinishReasonToolCalls}}
}

func TestStrategiesRejectUnresolvedDelegateSubtrees(t *testing.T) {
	for _, strategy := range []string{"interaction", "planning"} {
		t.Run(strategy, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			textSchema := contractValue(agent.SchemaFor[string]())
			gate := contractDeployment(contractValue(coordination.NewInputGate(coordination.InputGateConfig{Name: "contract.winner", Description: "Wait for explicit completion.", RequestSchema: textSchema, AnswerSchema: textSchema})), nil)
			var loserCalls atomic.Int32
			loser := contractInteraction(interaction.DefinitionConfig{Name: "contract.loser", Description: "Retain an unknown remote result.", MaxModelCalls: 1}, chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				loserCalls.Add(1)
				return nil, errors.New("remote result lost")
			}))
			race := contractDeployment(contractValue(coordination.NewFirstSuccess(coordination.FirstSuccessConfig{Name: "contract.competition", Description: "Choose a result before all remote effects are known.", MaxCandidates: 2, Accept: func(context.Context, agent.ChildOutcome) (bool, error) { return true, nil }})), nil)
			childBudget := agent.Budget{Steps: 16, Effects: 8, Signals: 16}
			candidates := []agent.ChildSpec{
				{Key: contractValue(agent.ParseChildKey("winner")), DeploymentRef: gate.DeploymentRef(), Input: contractValue(agent.EncodePayload("finish")), Budget: childBudget},
				{Key: contractValue(agent.ParseChildKey("loser")), DeploymentRef: loser.DeploymentRef(), Input: contractValue(agent.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})), Budget: childBudget},
			}
			prepare := contractValue(workflow.Transform("candidates", func(context.Context, struct{}) ([]agent.ChildSpec, error) { return candidates, nil }))
			call := contractValue(workflow.Call(workflow.CallConfig{ID: "race", Deployment: race, Budget: agent.Budget{Steps: 64, Effects: 48, Signals: 64}}))
			delegate := contractDeployment(contractValue(workflow.NewDefinition(workflow.DefinitionConfig{Name: "contract.composite", Description: "Reject unsafe competition output.", Stages: []workflow.Stage{prepare, call}})), nil)
			var rootCalls atomic.Int32
			var root agent.Deployment
			var input agent.Payload
			delegateBudget := agent.Budget{Steps: 96, Effects: 80, Signals: 96}
			if strategy == "interaction" {
				binding := contractValue(interaction.NewDelegate(interaction.DelegateConfig{Name: "delegate", Description: "Run the composite delegate.", Deployment: delegate, Budget: delegateBudget}))
				root = contractInteraction(interaction.DefinitionConfig{Name: "contract.parent", Description: "Reject unresolved delegate subtrees.", MaxModelCalls: 2, Delegates: []interaction.Delegate{binding}}, chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					if rootCalls.Add(1) == 1 {
						return contractToolCall("delegate", `{}`), nil
					}
					return contractText("incorrectly continued"), nil
				}))
				input = contractValue(agent.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run delegate"))}}))
			} else {
				done := contractValue(planning.NewCondition("world.done", planning.True))
				action := contractValue(planning.NewAction(planning.ActionConfig{Name: "action.delegate", Description: "Run the composite delegate.", Effects: []planning.Condition{done}}))
				binding := contractValue(planning.NewChildBinding(planning.ChildBindingConfig{Action: action, DeploymentRef: delegate.DeploymentRef(), Budget: delegateBudget}))
				definition := contractValue(planning.NewDefinition(planning.DefinitionConfig{Name: "contract.parent", Description: "Stop before sensing after unresolved child work.", InputSchema: contractValue(agent.SchemaFor[struct{}]()), Goal: contractValue(planning.NewGoal(planning.GoalConfig{Name: "goal.done", Description: "Complete the task.", Conditions: []planning.Condition{done}})), Actions: []planning.ActionBinding{binding}, Planner: goap.New(goap.Config{}), MaxActionAttempts: 2}))
				dispatcher := contractValue(planning.NewDispatcher(definition, planning.DispatcherConfig{Sensor: planning.SensorFunc(func(context.Context, planning.SenseRequest) (planning.WorldState, error) {
					rootCalls.Add(1)
					return planning.NewWorldState()
				})}))
				root = contractDeployment(definition, dispatcher)
				input = contractValue(agent.EncodePayload(struct{}{}))
			}
			resolver := contractResolver{gate.DeploymentRef(): gate, loser.DeploymentRef(): loser, race.DeploymentRef(): race, delegate.DeploymentRef(): delegate}
			events := &agenttest.ObservationRecorder{}
			engine := contractValue(agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver, EventListeners: []agent.EventListener{events}}))
			defer engine.Close(context.WithoutCancel(ctx))
			process := contractValue(engine.Start(ctx, root, input))
			defer process.Kill(context.WithoutCancel(ctx), "test cleanup")
			unknownEvent, err := events.AwaitEvent(ctx, func(event agent.Event) bool {
				fact, ok := event.EffectFinished()
				return ok && fact.SettlementStatus() == agent.SettlementStatusUnknown
			})
			if err != nil {
				t.Fatal(err)
			}
			var gateProcess *agent.Process
			var waitID agent.WaitID
			for !waitID.Valid() && ctx.Err() == nil {
				tree := contractValue(engine.CaptureTree(ctx, process.ID()))
				for _, child := range tree.ProcessSnapshots() {
					if child.DeploymentRef() == gate.DeploymentRef() {
						waitID, _ = child.WaitID()
						gateProcess, _ = engine.Process(child.ProcessID())
					}
				}
				runtime.Gosched()
			}
			if !waitID.Valid() {
				t.Fatal(ctx.Err())
			}
			answer := contractValue(agent.NewSignalRequest(contractValue(agent.ParseSignalID("signal:complete-winner")), waitID, json.RawMessage(`"done"`)))
			if accepted, err := gateProcess.DeliverSignals(ctx, answer); err != nil || !accepted {
				t.Fatalf("answer accepted=%v error=%v", accepted, err)
			}
			result := contractValue(process.Await(ctx))
			if err := process.Join(ctx); err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			wantCode := "interaction.delegate.unresolved_effects"
			if strategy == "planning" {
				wantCode = "planning.child.unresolved_effects"
			}
			effectID, _ := unknownEvent.EffectID()
			if !failed || failure.Code() != wantCode || rootCalls.Load() != 1 || !strings.Contains(failure.Message(), effectID.String()) || !strings.Contains(failure.Message(), unknownEvent.Relation().ProcessID().String()) {
				t.Fatalf("termination=%+v root calls=%d", result.Termination(), rootCalls.Load())
			}
			snapshot := contractValue(engine.CaptureTree(ctx, process.ID()))
			var failedDelegates, completedCompetitions, unresolvedEffects int
			for _, child := range snapshot.ProcessSnapshots() {
				unresolvedEffects += len(child.UnknownEffectIDs())
				if child.DeploymentRef() == race.DeploymentRef() && child.Status() == agent.StatusCompleted {
					completedCompetitions++
				}
				if child.DeploymentRef() == delegate.DeploymentRef() {
					delegateProcess, found := engine.Process(child.ProcessID())
					if !found {
						t.Fatal("direct delegate Process is missing")
					}
					delegateResult := contractValue(delegateProcess.Await(ctx))
					delegateFailure, failed := delegateResult.Termination().Failure()
					if child.Status() != agent.StatusFailed || !failed || delegateFailure.Code() != "workflow.call.unresolved_effects" {
						t.Fatalf("direct delegate termination=%+v", delegateResult.Termination())
					}
					failedDelegates++
				}
			}
			if failedDelegates != 1 || completedCompetitions != 1 || unresolvedEffects != 1 {
				t.Fatalf("subtree evidence: failed delegates=%d completed competitions=%d unresolved Effects=%d", failedDelegates, completedCompetitions, unresolvedEffects)
			}
			restoredEngine := contractValue(agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver}))
			defer restoredEngine.Close(context.WithoutCancel(ctx))
			restored := contractValue(restoredEngine.RestoreTree(ctx, root, snapshot))
			restoredResult := contractValue(restored.Await(ctx))
			restoredFailure, _ := restoredResult.Termination().Failure()
			if restoredFailure.Code() != wantCode || rootCalls.Load() != 1 || loserCalls.Load() != 1 {
				t.Fatalf("restore changed facts or replayed work: %+v", restoredResult)
			}
		})
	}
}
