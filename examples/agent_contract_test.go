package examples_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/planning/goap"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
)

const contractBuild = "cross-strategy-contract"

func contractValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

type contractResolver struct {
	programs []run.Program[run.Payload, run.Payload]
}

func (c *contractResolver) Resolve(reference run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	for _, program := range c.programs {
		if program.ProgramRef() == reference {
			return program, nil
		}
	}
	return run.Program[run.Payload, run.Payload]{}, fmt.Errorf("unresolved test program: %v", reference)
}
func contractInteraction(compiler *run.Compiler, config interaction.Config, model chat.Model) run.Program[interaction.Input, interaction.Output] {
	config.Model, config.Revision = model, "contract"
	return contractValue(interaction.Compile(compiler, config))
}
func contractText(text string) *chat.Response {
	return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart(text))), FinishReason: chat.FinishReasonStop}}
}
func contractToolCall(name, arguments string) *chat.Response {
	return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: name, Arguments: arguments}))), FinishReason: chat.FinishReasonToolCalls}}
}

func awaitContractRecord[O any](t *testing.T, ctx context.Context, handle *run.Handle[O], matches func(run.Record) bool) run.Entry {
	t.Helper()
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if matches(entry.Record) {
			return entry
		}
	}
	t.Fatal("root ended before the required committed record")
	return run.Entry{}
}

func TestStrategiesDrainUnknownDelegateSubtreesAcrossRecovery(t *testing.T) {
	for _, strategy := range []string{"interaction", "planning"} {
		t.Run(strategy, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			store := run.NewMemoryStore()
			resolver := new(contractResolver)
			runtime := contractValue(run.New(ctx, run.Config{Store: store, Build: contractBuild, ProgramResolver: resolver}))
			defer runtime.Close(context.WithoutCancel(ctx))
			compiler := runtime.Compiler()
			schema := contractValue(jsonschema.For[string]())
			gate := contractValue(coordination.Compile[string, run.Signal](compiler, coordination.Config{Name: "contract.winner", Description: "Wait for explicit completion.", Policy: coordination.InputGate{RequestSchema: schema, AnswerSchema: schema}}))
			var loserCalls atomic.Int32
			loser := contractInteraction(compiler, interaction.Config{Name: "contract.loser", Description: "Retain an unknown remote outcome.", MaxModelCalls: run.NewQuota(1)}, chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				loserCalls.Add(1)
				return nil, errors.New("remote result lost")
			}))
			race := contractValue(coordination.Compile[[]run.ChildSpec, coordination.FirstSuccessResult](compiler, coordination.Config{Name: "contract.competition", Description: "Drain every candidate before publishing a winner.", Policy: coordination.FirstSuccess{MaxCandidates: 2, Revision: "accept-completed", Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) { return true, nil }}}))
			childBudget := run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(8), Messages: run.NewQuota(16)}
			candidates := []run.ChildSpec{
				{Key: contractValue(run.ParseChildKey("winner")), ProgramRef: gate.ProgramRef(), Input: contractValue(run.EncodePayload("finish")), Budget: childBudget},
				{Key: contractValue(run.ParseChildKey("loser")), ProgramRef: loser.ProgramRef(), Input: contractValue(run.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})), Budget: childBudget},
			}
			prepare := contractValue(workflow.Transform("candidates", func(context.Context, struct{}) ([]run.ChildSpec, error) { return candidates, nil }))
			call := contractValue(workflow.Call(workflow.CallConfig{ID: "race", Program: race.Erase(), Budget: run.Budget{Steps: run.NewQuota(64), Effects: run.NewQuota(48), Messages: run.NewQuota(64)}}))
			delegate := contractValue(workflow.Compile[struct{}, coordination.FirstSuccessResult](compiler, workflow.Config{Name: "contract.composite", Description: "Publish a drained competition result.", Stages: []workflow.Stage{prepare, call}, Revision: "fixed-candidates"}))
			delegateBudget := run.Budget{Steps: run.NewQuota(96), Effects: run.NewQuota(80), Messages: run.NewQuota(96)}
			var rootCalls atomic.Int32
			var program run.Program[run.Payload, run.Payload]
			var input run.Payload
			if strategy == "interaction" {
				binding := contractValue(interaction.NewDelegate(interaction.DelegateConfig{Name: "delegate", Description: "Run the composite delegate.", Program: delegate.Erase(), Budget: delegateBudget}))
				program = contractInteraction(compiler, interaction.Config{Name: "contract.parent", Description: "Continue only after delegate quiescence.", MaxModelCalls: run.NewQuota(2), Delegates: []interaction.Delegate{binding}}, chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					if rootCalls.Add(1) == 1 {
						return contractToolCall("delegate", `{}`), nil
					}
					return contractText("drained"), nil
				})).Erase()
				input = contractValue(run.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run delegate"))}}))
			} else {
				done := contractValue(planning.NewCondition("world.done", planning.TruthTrue))
				action := contractValue(planning.NewAction(planning.ActionConfig{Name: "action.delegate", Description: "Run the composite delegate.", Effects: []planning.Condition{done}}))
				binding := contractValue(planning.NewChildBinding(planning.ChildBindingConfig{Action: action, Program: delegate.Erase(), Budget: delegateBudget}))
				program = contractValue(planning.Compile[struct{}](compiler, planning.Config{Name: "contract.parent", Description: "Sense only after child quiescence.", Goal: contractValue(planning.NewGoal(planning.GoalConfig{Name: "goal.done", Description: "Complete the task.", Conditions: []planning.Condition{done}})), Actions: []planning.ActionBinding{binding}, Planner: goap.New(goap.Config{}), MaxActionAttempts: run.NewQuota(2), Revision: "sense-after-child", Sensor: planning.SensorFunc(func(context.Context, planning.SenseRequest) (planning.WorldState, error) {
					if rootCalls.Add(1) > 1 {
						return planning.NewWorldState(done)
					}
					return planning.NewWorldState()
				})})).Erase()
				input = contractValue(run.EncodePayload(struct{}{}))
			}
			resolver.programs = []run.Program[run.Payload, run.Payload]{gate.Erase(), loser.Erase()}
			handle := contractValue(runtime.Start(ctx, program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: rand.Text()}, Input: input}))
			unknown := awaitContractRecord(t, ctx, handle, func(record run.Record) bool {
				settled, ok := record.(run.EffectSettled)
				return ok && settled.Outcome.Status() == run.SettlementStatusUnknown
			}).Record.(run.EffectSettled)
			awaitContractRecord(t, ctx, handle, func(record run.Record) bool {
				step, ok := record.(run.Stepped)
				return ok && len(step.Transition.Waits()) > 0
			})
			view := contractValue(handle.Inspect(ctx))
			for _, process := range view.Processes {
				if process.Program == gate.ProgramRef() {
					owner, ok := runtime.Process(process.ID)
					if !ok || len(process.Waits) != 1 {
						t.Fatal("input gate owner missing")
					}
					if err := owner.Answer(ctx, process.Waits[0].Key, "done"); err != nil {
						t.Fatal(err)
					}
				}
			}
			awaitContractRecord(t, ctx, handle, func(record run.Record) bool {
				control, ok := record.(run.ControlRequested)
				return ok && control.Process == unknown.ID.Process && control.Kind == run.ControlCancel
			})
			view = contractValue(handle.Inspect(ctx))
			root, _ := view.Process(handle.ID())
			if root.Termination.Valid() || rootCalls.Load() != 1 {
				t.Fatal("parent continued before unknown descendant drained", rootCalls.Load())
			}
			if err := runtime.Close(ctx); err != nil {
				t.Fatal(err)
			}
			replacement := contractValue(run.New(ctx, run.Config{Store: store, Build: contractBuild, ProgramResolver: resolver}))
			defer replacement.Close(context.WithoutCancel(ctx))
			recovered := contractValue(replacement.Recover(ctx, program, handle.ID()))
			view = contractValue(recovered.Inspect(ctx))
			owner, ok := view.Process(unknown.ID.Process)
			if !ok || len(owner.PendingEffects) != 1 {
				t.Fatal("recovery lost pending descendant")
			}
			settlement := contractValue(interaction.SettleModelResult(loser, owner.PendingEffects[0].Request, contractText("investigated outcome")))
			if err := recovered.ResolveUnknownEffect(ctx, unknown.ID, settlement); err != nil {
				t.Fatal(err)
			}
			result := contractValue(recovered.Await(ctx))
			if result.Termination().Status() != run.StatusCompleted || rootCalls.Load() != 2 || loserCalls.Load() != 1 {
				t.Fatalf("drained result=%v root=%d loser=%d", result.Termination(), rootCalls.Load(), loserCalls.Load())
			}
			log, found, err := store.Load(ctx, handle.ID())
			if err != nil || !found {
				t.Fatal(found, err)
			}
			exits := make(map[run.ProcessID]run.LSN)
			var raceID, delegateID run.ProcessID
			for _, entry := range log.Entries {
				switch record := entry.Record.(type) {
				case run.Started:
					if record.Program == race.ProgramRef() {
						raceID = record.Process
					}
					if record.Program == delegate.ProgramRef() {
						delegateID = record.Process
					}
				case run.Exited:
					exits[record.Process] = entry.LSN
				}
			}
			if exits[unknown.ID.Process] == 0 || exits[unknown.ID.Process] >= exits[raceID] || exits[raceID] >= exits[delegateID] || exits[delegateID] >= exits[handle.ID()] {
				t.Fatalf("exit order did not drain child subtrees: %v", exits)
			}
		})
	}
}
