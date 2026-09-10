package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
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
	definition := require(interaction.NewDefinition(interaction.DefinitionConfig{Name: name, Description: name, MaxModelCalls: 3}))
	client := require(chatclient.New(model, chatclient.Config{}))
	dispatcher := require(interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: client}))
	return require(agent.NewDeployment(agent.DeploymentConfig{Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("test-model-code")), ConfigurationDigest: agent.ComputeDigest([]byte(name))}))
}

func TestCoordinatorWaitIncludesResultsArrivingDuringItsModelCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		model := modelDeployment("test.coordinator_model", modelFunc(func(ctx context.Context, modelRequest *chat.Request) (*chat.Response, error) {
			turn := require(require(agent.ParseInput([]byte(modelRequest.Messages[0].Text()))).Decode[Turn]())
			var decision Decision
			switch turn.Number {
			case 1:
				decision = Decision{Mode: Continue, State: turn.State, Tasks: []TaskRequest{request("input", "test.gate", "answer")}}
			case 2:
				if len(turn.Tasks) != 1 || turn.Tasks[0].Outcome != nil {
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
				decision = finish(turn, "observed during decision")
			default:
				return nil, errors.New("unexpected turn")
			}
			return textResponse(string(require(json.Marshal(decision))))
		}))
		render := require(workflow.Transform("render", func(_ context.Context, turn Turn) (interaction.Input, error) {
			return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(require(json.Marshal(turn)))))}}, nil
		}))
		call := require(workflow.Call(workflow.CallConfig{ID: "call", Deployment: model, Budget: agent.Budget{Steps: 16, Effects: 8, Signals: 16}}))
		decode := require(workflow.Transform("decode", func(_ context.Context, output interaction.Output) (Decision, error) {
			if output.ModelResponse == nil {
				return Decision{}, errors.New("missing model response")
			}
			value, err := agent.ParseOutput([]byte(output.ModelResponse.Text()))
			if err != nil {
				return Decision{}, err
			}
			return value.Decode[Decision]()
		}))
		coordinator := binding(require(workflow.NewDefinition(workflow.DefinitionConfig{Name: "test.model_coordinator", Description: "Adapt a model decision.", Stages: []workflow.Stage{render, call, decode}})))
		config, deployments := fixtureConfig(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "unused"), nil }, gate())
		delete(deployments, config.Coordinator.Deployment.DeploymentRef())
		config.Coordinator = WorkerConfig{Deployment: coordinator, Budget: agent.Budget{Steps: 64, Effects: 32, Signals: 64}}
		definition := require(NewDefinition(config))
		deployments[model.DeploymentRef()], deployments[coordinator.DeploymentRef()] = model, coordinator
		engine, process := run(t, definition, deployments, agenttest.NewMemoryTreeDurability())
		<-entered
		synctest.Wait()
		tree := require(engine.InspectTree(t.Context(), process.ID()))
		for _, fact := range tree.Processes {
			if fact.Snapshot.DeploymentRef().Name() != "test.gate" {
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
		unblock()
		if got := completed(t, process); got != "observed during decision" {
			t.Fatal(got)
		}
	})
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
				input := require(agent.EncodeInput(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("initial"))}}))
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
			DeploymentResolver: deployments, TreeDurability: agenttest.NewMemoryTreeDurability(),
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
