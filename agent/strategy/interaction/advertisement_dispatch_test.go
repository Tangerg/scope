package interaction

import (
	"context"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestRestoredAdvertisementsDispatchAgainstBoundManifest(t *testing.T) {
	for _, matched := range []bool{true, false} {
		t.Run(map[bool]string{true: "bound", false: "unbound"}[matched], func(t *testing.T) {
			definition := advertisementTestDefinition(t)
			state, err := encodeState(executionState{
				Phase: phaseReadyModel, ModelCallCount: 1, AdvertisedToolNames: []string{"first", "second"},
				WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("resume"))}},
			})
			if err != nil {
				t.Fatal(err)
			}
			dispatcherDefinition := definition
			if !matched {
				dispatcherDefinition, err = NewDefinition(DefinitionConfig{Name: "interaction.empty", Description: "Exercise missing bindings.", MaxModelCalls: 2})
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			dispatcher, err := NewDispatcher(dispatcherDefinition, DispatcherConfig{Model: chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				calls++
				var names []string
				for _, entry := range request.Tools {
					names = append(names, entry.Name)
				}
				if !slices.Equal(names, []string{"initial", "first", "second"}) {
					t.Errorf("model tools = %v", names)
				}
				message := chat.NewAssistantMessage(chat.NewTextPart("done"))
				return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			deployment, err := agent.NewDeployment(agent.DeploymentConfig{
				Definition: restoredAdvertisementDefinition{Definition: definition, state: state}, Dispatcher: dispatcher,
				ImplementationDigest: agent.ComputeDigest([]byte("advertisement-dispatch")), ConfigurationDigest: agent.ComputeDigest([]byte("advertisement-config")),
			})
			if err != nil {
				t.Fatal(err)
			}
			engine, err := agent.NewEngine(agent.EngineConfig{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			input, err := agent.EncodeInput(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("resume"))}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Run(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			if matched {
				if result.Status() != agent.StatusCompleted || calls != 1 {
					t.Fatalf("status = %s, calls = %d", result.Status(), calls)
				}
			} else {
				failure, present := result.Termination().Failure()
				if result.Status() != agent.StatusFailed || !present || failure.Code() != "interaction.host.failed" || calls != 0 {
					t.Fatalf("local rejection lost known outcome: termination = %+v, failure = %+v, calls = %d", result.Termination(), failure, calls)
				}
			}
		})
	}
}

type restoredAdvertisementDefinition struct {
	*Definition
	state agent.ExecutionState
}

func (r restoredAdvertisementDefinition) Start(agent.Input) (agent.Execution, error) {
	return r.Restore(r.state)
}
