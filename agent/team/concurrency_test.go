package team

import (
	startrand "crypto/rand"

	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

type modelFunc func(context.Context, *chat.Request) (*chat.Response, error)

func (m modelFunc) Call(ctx context.Context, modelRequest *chat.Request) (*chat.Response, error) {
	return m(ctx, modelRequest)
}
func textResponse(text string) (*chat.Response, error) {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, nil)
}
func modelDeployment(name string, model chat.Model) agentrun.Program[agentrun.Payload, agentrun.Payload] {
	definition := require(agent.NewDefinition(agent.DefinitionConfig{Name: name, Description: name, MaxModelCalls: agentrun.NewQuota(3)}))
	client := require(chatclient.New(model, chatclient.Config{}))
	dispatcher := require(agent.NewDispatcher(definition, agent.DispatcherConfig{Model: client}))
	return require(agentrun.Bind[agentrun.Payload, agentrun.Payload](agentrun.BindingConfig{Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agentrun.ComputeDigest([]byte("test-model-code")), ConfigurationDigest: agentrun.ComputeDigest([]byte(name))}))
}

func TestCoordinatorWaitIncludesResultsArrivingDuringItsModelCall(t *testing.T) {
	for _, mode := range []string{"memory", "restored"} {
		for _, count := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/tasks_%d", mode, count), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var observed bool
					entered, release := make(chan struct{}), make(chan struct{})
					unblock := sync.OnceFunc(func() { close(release) })
					defer unblock()
					model := modelDeployment("test.coordinator_model", modelFunc(func(ctx context.Context, modelRequest *chat.Request) (*chat.Response, error) {
						turn := require(require(agentrun.ParsePayload([]byte(modelRequest.Messages[0].Text()))).Decode[Turn]())
						var decision Decision
						switch turn.Number {
						case 1:
							tasks := []TaskRequest{request("input", "test.gate", "answer")}
							if count == 2 {
								tasks = append(tasks, request("other", "test.gate", "waiting"))
							}
							decision = Decision{Mode: ModeContinue, State: turn.State, Tasks: tasks}
						case 2:
							if len(turn.Tasks) != count || turn.Tasks[0].Outcome != nil {
								return nil, errors.New("task already observed before held decision")
							}
							close(entered)
							select {
							case <-release:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
							decision = Decision{Mode: ModeWait, State: turn.State}
						case 3:
							if turn.Tasks[0].Outcome == nil {
								return nil, errors.New("completion during model call was lost")
							}
							if count == 2 && turn.Tasks[1].Outcome != nil {
								return nil, errors.New("other task unexpectedly completed")
							}
							observed = true
							decision = finish(turn, "observed during decision")
						default:
							return nil, errors.New("unexpected turn")
						}
						return textResponse(string(require(jsonv2.Marshal(decision))))
					}))
					render := require(workflow.Transform("render", func(_ context.Context, turn Turn) (agent.Input, error) {
						return agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(require(jsonv2.Marshal(turn)))))}}, nil
					}))
					call := require(workflow.Call(workflow.CallConfig{ID: "call", Program: model, Budget: agentrun.Budget{Steps: agentrun.NewQuota(16), Effects: agentrun.NewQuota(8), Signals: agentrun.NewQuota(16)}}))
					decode := require(workflow.Transform("decode", func(_ context.Context, output agent.Output) (Decision, error) {
						return chatclient.JSON[Decision]().Decode(output.ModelResponse)
					}))
					coordinator := binding(require(workflow.NewDefinition(workflow.DefinitionConfig{Name: "test.model_coordinator", Description: "Adapt a model decision.", Stages: []workflow.Stage{render, call, decode}})))
					config := fixtureConfig(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "unused"), nil }, gate())
					config.Coordinator = WorkerConfig{Program: coordinator, Budget: agentrun.Budget{Steps: agentrun.NewQuota(64), Effects: agentrun.NewQuota(32), Signals: agentrun.NewQuota(64)}}
					definition := require(NewDefinition(config))
					store := agentrun.NewMemoryStore()
					var committer agentrun.Store = store
					if mode == "restored" {
						committer = &coordinatorSettlementCrash{MemoryStore: store}
					}
					engine, process := run(t, definition, committer)
					<-entered
					synctest.Wait()
					tree := require(engine.InspectTree(t.Context(), process.Relation().ProcessID()))
					for _, fact := range tree.Processes {
						key, _ := fact.Snapshot.Relation().ChildKey()
						if fact.Snapshot.ProgramRef().Name() != "test.gate" || key.String() != "input" {
							continue
						}
						wait, present := fact.Snapshot.WaitID()
						if !present {
							t.Fatal("gate has not opened")
						}
						child, _ := engine.Process(fact.Snapshot.Relation().ProcessID())
						signal := require(agentrun.NewSignalRequest(require(agentrun.ParseSignalID("signal:during-decision")), wait, []byte(`"answer"`)))
						if accepted, err := child.DeliverSignals(t.Context(), signal); err != nil || !accepted {
							t.Fatalf("input=%t %v", accepted, err)
						}
					}
					synctest.Wait()
					if mode == "restored" {
						inspection := require(engine.InspectTree(t.Context(), process.Relation().ProcessID()))
						root, _ := inspection.Process(process.Relation().ProcessID())
						var state executionState
						if err := jsonv2.Unmarshal(root.Snapshot.CommittedExecutionState().Payload(), &state); err != nil {
							t.Fatal(err)
						}
						if state.Turn.Outcome != nil || state.Tasks[0].Outcome == nil || !state.hasUnseenOutcome() {
							t.Fatal("crash cut does not retain an unseen worker outcome")
						}
					}
					unblock()
					if mode == "restored" {
						if result, err := process.Await(t.Context()); result.Valid() || err == nil {
							t.Fatalf("crash result=%v err=%v", result.Termination().Status(), err)
						}
						head, found, err := store.LoadTree(t.Context(), process.Relation().ProcessID())
						if err != nil || !found {
							t.Fatalf("head=%t err=%v", found, err)
						}
						var restoredHead agentrun.TreeSnapshot
						if err := jsonv2.Unmarshal(head.JSON(), &restoredHead); err != nil {
							t.Fatal(err)
						}
						restoredEngine := require(agentrun.New(t.Context(), agentrun.Config{Store: store}))
						defer func() {
							if err := restoredEngine.Close(t.Context()); err != nil {
								t.Error(err)
							}
						}()
						process = require(restoredEngine.Recover(t.Context(), binding(definition), restoredHead))
					}
					synctest.Wait()
					if !observed {
						t.Fatal("coordinator did not consume the new outcome while another task remained active")
					}
					if got := completed(t, process); got != "observed during decision" {
						t.Fatal(got)
					}
				})
			})
		}
	}
}

func TestCoordinatorSteersInteractionThroughItsCanonicalSignalContract(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		worker := modelDeployment("test.interaction", modelFunc(func(ctx context.Context, modelRequest *chat.Request) (*chat.Response, error) {
			if len(modelRequest.Messages) == 1 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if modelRequest.Messages[0].Text() != "initial" {
					return nil, errors.New("in-flight request changed")
				}
				return textResponse("draft")
			}
			if modelRequest.Messages[len(modelRequest.Messages)-1].Text() != "revise" {
				return nil, errors.New("steer was not applied to next request")
			}
			return textResponse("revised")
		}))
		definition := fixture(func(_ context.Context, turn Turn) (Decision, error) {
			switch turn.Number {
			case 1:
				input := require(agentrun.EncodePayload(agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("initial"))}}))
				return Decision{Mode: ModeContinue, State: turn.State, Tasks: []TaskRequest{{Key: require(agentrun.ParseChildKey("writer")), Worker: "test.interaction", Input: input}}}, nil
			case 2:
				signal := require(agent.NewSteerSignal(require(agentrun.ParseSignalID("signal:revise")), chat.NewUserMessage(chat.NewTextPart("revise"))))
				return Decision{Mode: ModeWait, State: turn.State, Controls: []Control{{Task: turn.Tasks[0].Request.Key, Signal: &signal}}}, nil
			default:
				if turn.Number != 3 || turn.Controls[0].Result == nil || turn.Tasks[0].Outcome == nil {
					return Decision{}, errors.New("missing steer outcome")
				}
				output, _ := turn.Tasks[0].Outcome.Result().Termination().Output()
				response := require(output.Decode[agent.Output]())
				if response.ModelCalls != 2 {
					return Decision{}, errors.New("invalid model continuation")
				}
				text, err := chatclient.Text().Decode(response.ModelResponse)
				if err != nil {
					return Decision{}, err
				}
				return finish(turn, text), nil
			}
		}, worker)
		engine := require(agentrun.New(t.Context(), agentrun.Config{
			Store: agentrun.NewMemoryStore(),
			ProcessAdmitter: agentrun.ProcessAdmitterFunc(func(ctx context.Context, admission agentrun.ProcessAdmission) error {
				if key, child := admission.Relation().ChildKey(); child && key.String() == "collaboration.turn.2" {
					select {
					case <-entered:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}),
		}))
		t.Cleanup(func() {
			if err := engine.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
		process := require(engine.Start(t.Context(), binding(definition), agentrun.StartRequest[agentrun.Payload]{ID: agentrun.StartID{Scope: "test", Key: startrand.Text()}, Input: input("initial")}))
		<-entered
		synctest.Wait()
		unblock()
		if got := completed(t, process); got != "revised" {
			t.Fatal(got)
		}
	})
}

// Stop after the second coordinator's model result is durable but before the
// collaboration can adopt its decision. Worker outcomes are already committed.
type coordinatorSettlementCrash struct {
	*agentrun.MemoryStore
}

func (c *coordinatorSettlementCrash) CommitEffect(ctx context.Context, boundary agentrun.EffectBoundary) error {
	if err := c.MemoryStore.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	if boundary.Kind() != agentrun.EffectBoundaryKindSettled || boundary.Request().ProgramRef().Name() != "test.coordinator_model" {
		return nil
	}
	parent, _ := boundary.Request().Relation().ParentID()
	for _, snapshot := range boundary.TreeSnapshot().ProcessSnapshots() {
		key, _ := snapshot.Relation().ChildKey()
		if snapshot.Relation().ProcessID() == parent && key.String() == "collaboration.turn.2" {
			return errors.New("crash after coordinator settlement")
		}
	}
	return nil
}
