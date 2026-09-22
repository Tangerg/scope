package collaboration

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/workflow"
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
func modelDeployment(name string, model chat.Model) agent.Deployment {
	definition := require(interaction.NewDefinition(interaction.DefinitionConfig{Name: name, Description: name, MaxModelCalls: agent.NewQuota(3)}))
	client := require(chatclient.New(model, chatclient.Config{}))
	dispatcher := require(interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: client}))
	return require(agent.NewDeployment(agent.DeploymentConfig{Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("test-model-code")), ConfigurationDigest: agent.ComputeDigest([]byte(name))}))
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
						turn := require(require(agent.ParsePayload([]byte(modelRequest.Messages[0].Text()))).Decode[Turn]())
						var decision Decision
						switch turn.Number {
						case 1:
							tasks := []TaskRequest{request("input", "test.gate", "answer")}
							if count == 2 {
								tasks = append(tasks, request("other", "test.gate", "waiting"))
							}
							decision = Decision{Mode: Continue, State: turn.State, Tasks: tasks}
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
							decision = Decision{Mode: Wait, State: turn.State}
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
					render := require(workflow.Transform("render", func(_ context.Context, turn Turn) (interaction.Input, error) {
						return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(require(jsonv2.Marshal(turn)))))}}, nil
					}))
					call := require(workflow.Call(workflow.CallConfig{ID: "call", Deployment: model, Budget: agent.Budget{Steps: agent.NewQuota(16), Effects: agent.NewQuota(8), Signals: agent.NewQuota(16)}}))
					decode := require(workflow.Transform("decode", func(_ context.Context, output interaction.Output) (Decision, error) {
						if output.ModelResponse == nil {
							return Decision{}, errors.New("missing model response")
						}
						value, err := agent.ParsePayload([]byte(output.ModelResponse.Text()))
						if err != nil {
							return Decision{}, err
						}
						return value.Decode[Decision]()
					}))
					coordinator := binding(require(workflow.NewDefinition(workflow.DefinitionConfig{Name: "test.model_coordinator", Description: "Adapt a model decision.", Stages: []workflow.Stage{render, call, decode}})))
					config, deployments := fixtureConfig(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "unused"), nil }, gate())
					delete(deployments, config.Coordinator.Deployment.DeploymentRef())
					config.Coordinator = WorkerConfig{Deployment: coordinator, Budget: agent.Budget{Steps: agent.NewQuota(64), Effects: agent.NewQuota(32), Signals: agent.NewQuota(64)}}
					definition := require(NewDefinition(config))
					deployments[model.DeploymentRef()], deployments[coordinator.DeploymentRef()] = model, coordinator
					store := agent.NewMemoryTreeCommitter()
					var committer agent.TreeCommitter = store
					if mode == "restored" {
						committer = &coordinatorSettlementCrash{MemoryTreeCommitter: store}
					}
					engine, process := run(t, definition, deployments, committer)
					<-entered
					synctest.Wait()
					tree := require(engine.InspectTree(t.Context(), process.ID()))
					for _, fact := range tree.Processes {
						key, _ := fact.Snapshot.Relation().ChildKey()
						if fact.Snapshot.DeploymentRef().Name() != "test.gate" || key.String() != "input" {
							continue
						}
						wait, present := fact.Snapshot.WaitID()
						if !present {
							t.Fatal("gate has not opened")
						}
						child, _ := engine.Process(fact.Snapshot.ProcessID())
						signal := require(agent.NewSignalRequest(require(agent.ParseSignalID("signal:during-decision")), wait, []byte(`"answer"`)))
						if accepted, err := child.DeliverSignals(t.Context(), signal); err != nil || !accepted {
							t.Fatalf("input=%t %v", accepted, err)
						}
					}
					synctest.Wait()
					if mode == "restored" {
						inspection := require(engine.InspectTree(t.Context(), process.ID()))
						root, _ := inspection.Process(process.ID())
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
							t.Fatalf("crash result=%v err=%v", result.Status(), err)
						}
						head, found, err := store.LoadTree(t.Context(), process.ID())
						if err != nil || !found {
							t.Fatalf("head=%t err=%v", found, err)
						}
						var restoredHead agent.TreeSnapshot
						if err := jsonv2.Unmarshal(head.JSON(), &restoredHead); err != nil {
							t.Fatal(err)
						}
						restoredEngine := require(agent.NewEngine(agent.EngineConfig{TreeCommitter: store, DeploymentResolver: deployments}))
						defer func() {
							if err := restoredEngine.Close(t.Context()); err != nil {
								t.Error(err)
							}
						}()
						process = require(restoredEngine.RestoreTree(t.Context(), binding(definition), restoredHead))
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
		definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
			switch turn.Number {
			case 1:
				input := require(agent.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("initial"))}}))
				return Decision{Mode: Continue, State: turn.State, Tasks: []TaskRequest{{Key: require(agent.ParseChildKey("writer")), Worker: "test.interaction", Input: input}}}, nil
			case 2:
				signal := require(interaction.NewSteerSignal(require(agent.ParseSignalID("signal:revise")), chat.NewUserMessage(chat.NewTextPart("revise"))))
				return Decision{Mode: Wait, State: turn.State, Controls: []Control{{Task: turn.Tasks[0].Request.Key, Signal: &signal}}}, nil
			default:
				if turn.Number != 3 || turn.Controls[0].Result == nil || turn.Tasks[0].Outcome == nil {
					return Decision{}, errors.New("missing steer outcome")
				}
				output, _ := turn.Tasks[0].Outcome.Result().Output()
				response := require(output.Decode[interaction.Output]())
				if response.ModelCalls != 2 || response.ModelResponse == nil {
					return Decision{}, errors.New("invalid model continuation")
				}
				return finish(turn, response.ModelResponse.Text()), nil
			}
		}, worker)
		engine := require(agent.NewEngine(agent.EngineConfig{
			DeploymentResolver: deployments, TreeCommitter: agent.NewMemoryTreeCommitter(),
			ProcessAdmitter: agent.ProcessAdmitterFunc(func(ctx context.Context, admission agent.ProcessAdmission) error {
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
		process := require(engine.Start(t.Context(), binding(definition), input("initial")))
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
	*agent.MemoryTreeCommitter
}

func (c *coordinatorSettlementCrash) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	if err := c.MemoryTreeCommitter.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	if boundary.Kind() != agent.EffectBoundaryKindSettled || boundary.Request().DeploymentRef().Name() != "test.coordinator_model" {
		return nil
	}
	parent, _ := boundary.Request().Relation().ParentID()
	for _, snapshot := range boundary.TreeSnapshot().ProcessSnapshots() {
		key, _ := snapshot.Relation().ChildKey()
		if snapshot.ProcessID() == parent && key.String() == "collaboration.turn.2" {
			return errors.New("crash after coordinator settlement")
		}
	}
	return nil
}
