package examples_test

import (
	startrand "crypto/rand"

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
	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/planning/goap"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
)

func contractValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func contractDeployment(definition run.Definition, dispatcher run.Dispatcher) run.Program[run.Payload, run.Payload] {
	return contractValue(run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("contract-test-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte(definition.Descriptor().Name())),
	}))
}

type contractResolver map[run.ProgramRef]run.Program[run.Payload, run.Payload]

func (c contractResolver) Resolve(ref run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	if deployment, ok := c[ref]; ok {
		return deployment, nil
	}
	return run.Program[run.Payload, run.Payload]{}, fmt.Errorf("unresolved test deployment: %v", ref)
}

func contractInteraction(config agent.DefinitionConfig, model chat.Model) run.Program[run.Payload, run.Payload] {
	definition := contractValue(agent.NewDefinition(config))
	return contractDeployment(definition, contractValue(agent.NewDispatcher(definition, agent.DispatcherConfig{Model: model})))
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
			textSchema := contractValue(jsonschema.For[string]())
			gate := contractDeployment(contractValue(coordination.NewInputGate(coordination.InputGateConfig{Name: "contract.winner", Description: "Wait for explicit completion.", RequestSchema: textSchema, AnswerSchema: textSchema})), nil)
			var loserCalls atomic.Int32
			loser := contractInteraction(agent.DefinitionConfig{Name: "contract.loser", Description: "Retain an unknown remote result.", MaxModelCalls: run.NewQuota(1)}, chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				loserCalls.Add(1)
				return nil, errors.New("remote result lost")
			}))
			race := contractDeployment(contractValue(coordination.NewFirstSuccess(coordination.FirstSuccessConfig{Name: "contract.competition", Description: "Choose a result before all remote effects are known.", MaxCandidates: 2, Accept: func(context.Context, run.ChildKey, run.ChildOutcome) (bool, error) { return true, nil }})), nil)
			childBudget := run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(8), Signals: run.NewQuota(16)}
			candidates := []run.ChildSpec{
				{Key: contractValue(run.ParseChildKey("winner")), ProgramRef: gate.ProgramRef(), Input: contractValue(run.EncodePayload("finish")), Budget: childBudget},
				{Key: contractValue(run.ParseChildKey("loser")), ProgramRef: loser.ProgramRef(), Input: contractValue(run.EncodePayload(agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})), Budget: childBudget},
			}
			prepare := contractValue(workflow.Transform("candidates", func(context.Context, struct{}) ([]run.ChildSpec, error) { return candidates, nil }))
			call := contractValue(workflow.Call(workflow.CallConfig{ID: "race", Deployment: race, Budget: run.Budget{Steps: run.NewQuota(64), Effects: run.NewQuota(48), Signals: run.NewQuota(64)}}))
			delegate := contractDeployment(contractValue(workflow.NewDefinition(workflow.DefinitionConfig{Name: "contract.composite", Description: "Reject unsafe competition output.", Stages: []workflow.Stage{prepare, call}})), nil)
			var rootCalls atomic.Int32
			var root run.Program[run.Payload, run.Payload]
			var input run.Payload
			delegateBudget := run.Budget{Steps: run.NewQuota(96), Effects: run.NewQuota(80), Signals: run.NewQuota(96)}
			if strategy == "interaction" {
				binding := contractValue(agent.NewDelegate(agent.DelegateConfig{Name: "delegate", Description: "Run the composite delegate.", Deployment: delegate, Budget: delegateBudget}))
				root = contractInteraction(agent.DefinitionConfig{Name: "contract.parent", Description: "Reject unresolved delegate subtrees.", MaxModelCalls: run.NewQuota(2), Delegates: []agent.Delegate{binding}}, chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					if rootCalls.Add(1) == 1 {
						return contractToolCall("delegate", `{}`), nil
					}
					return contractText("incorrectly continued"), nil
				}))
				input = contractValue(run.EncodePayload(agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run delegate"))}}))
			} else {
				done := contractValue(planning.NewCondition("world.done", planning.TruthTrue))
				action := contractValue(planning.NewAction(planning.ActionConfig{Name: "action.delegate", Description: "Run the composite delegate.", Effects: []planning.Condition{done}}))
				binding := contractValue(planning.NewChildBinding(planning.ChildBindingConfig{Action: action, Deployment: delegate, Budget: delegateBudget}))
				definition := contractValue(planning.NewDefinition(planning.DefinitionConfig{Name: "contract.parent", Description: "Stop before sensing after unresolved child work.", InputSchema: contractValue(jsonschema.For[struct{}]()), Goal: contractValue(planning.NewGoal(planning.GoalConfig{Name: "goal.done", Description: "Complete the task.", Conditions: []planning.Condition{done}})), Actions: []planning.ActionBinding{binding}, Planner: goap.New(goap.Config{}), MaxActionAttempts: run.NewQuota(2)}))
				dispatcher := contractValue(planning.NewDispatcher(definition, planning.DispatcherConfig{Sensor: planning.SensorFunc(func(context.Context, planning.SenseRequest) (planning.WorldState, error) {
					rootCalls.Add(1)
					return planning.NewWorldState()
				})}))
				root = contractDeployment(definition, dispatcher)
				input = contractValue(run.EncodePayload(struct{}{}))
			}
			// The race candidates arrive as FirstSuccess input; every other child is
			// bound by its parent's Program.
			resolver := contractResolver{gate.ProgramRef(): gate, loser.ProgramRef(): loser}
			events := &runtest.ObservationRecorder{}
			store := run.NewMemoryStore()
			engine := contractValue(run.New(t.Context(), run.Config{Store: store, ProgramResolver: resolver, EventListeners: []run.EventListener{events}}))
			defer engine.Close(context.WithoutCancel(ctx))
			process := contractValue(engine.Start(ctx, root, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input}))
			defer process.Kill(context.WithoutCancel(ctx), "test cleanup")
			unknownEvent, err := events.AwaitEvent(ctx, func(event run.Event) bool {
				fact, ok := event.EffectFinished()
				return ok && fact.SettlementStatus() == run.SettlementStatusUnknown
			})
			if err != nil {
				t.Fatal(err)
			}
			var gateProcess *run.Handle[run.Payload]
			var waitID run.WaitID
			for !waitID.Valid() && ctx.Err() == nil {
				tree := contractValue(engine.CaptureTree(ctx, process.Relation().ProcessID()))
				for _, child := range tree.ProcessSnapshots() {
					if child.ProgramRef() == gate.ProgramRef() {
						waitID, _ = child.WaitID()
						gateProcess, _ = engine.Process(child.Relation().ProcessID())
					}
				}
				runtime.Gosched()
			}
			if !waitID.Valid() {
				t.Fatal(ctx.Err())
			}
			answer := contractValue(run.NewSignalRequest(contractValue(run.ParseSignalID("signal:complete-winner")), waitID, json.RawMessage(`"done"`)))
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
			snapshot := contractValue(engine.CaptureTree(ctx, process.Relation().ProcessID()))
			var failedDelegates, completedCompetitions, unresolvedEffects int
			for _, child := range snapshot.ProcessSnapshots() {
				unresolvedEffects += len(child.UnknownEffectIDs())
				if child.ProgramRef() == race.ProgramRef() && child.Status() == run.StatusCompleted {
					completedCompetitions++
				}
				if child.ProgramRef() == delegate.ProgramRef() {
					delegateProcess, found := engine.Process(child.Relation().ProcessID())
					if !found {
						t.Fatal("direct delegate Process is missing")
					}
					delegateResult := contractValue(delegateProcess.Await(ctx))
					delegateFailure, failed := delegateResult.Termination().Failure()
					if child.Status() != run.StatusFailed || !failed || delegateFailure.Code() != "workflow.call.unresolved_effects" {
						t.Fatalf("direct delegate termination=%+v", delegateResult.Termination())
					}
					failedDelegates++
				}
			}
			if failedDelegates != 1 || completedCompetitions != 1 || unresolvedEffects != 1 {
				t.Fatalf("subtree evidence: failed delegates=%d completed competitions=%d unresolved Effects=%d", failedDelegates, completedCompetitions, unresolvedEffects)
			}
			restoredEngine := contractValue(run.New(t.Context(), run.Config{Store: store, ProgramResolver: resolver}))
			defer restoredEngine.Close(context.WithoutCancel(ctx))
			restored := contractValue(restoredEngine.Recover(ctx, root, snapshot))
			restoredResult := contractValue(restored.Await(ctx))
			restoredFailure, _ := restoredResult.Termination().Failure()
			if restoredFailure.Code() != wantCode || rootCalls.Load() != 1 || loserCalls.Load() != 1 {
				t.Fatalf("restore changed facts or replayed work: %+v", restoredResult)
			}
		})
	}
}
