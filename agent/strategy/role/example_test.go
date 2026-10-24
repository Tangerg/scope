package role_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/role"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func ExampleNew() {
	format := chatclient.JSON[greeting]()
	greeter := exampleValue(role.New(role.Config[greetRequest, greeting]{
		Name: "example.greeter", Description: "Greet a caller by name.",
		Interaction: greetDeployment(),
		BuildInput: func(_ context.Context, req greetRequest) (interaction.Input, error) {
			data, err := jsonv2.Marshal(req)
			if err != nil {
				return interaction.Input{}, err
			}
			return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(data)))}}, nil
		},
		DecodeOutput: func(_ context.Context, output interaction.Output) (greeting, error) {
			return format.Decode(output.ModelResponse)
		},
		Budget:               agent.Budget{Steps: agent.NewQuota(16), Effects: agent.NewQuota(16), Signals: agent.NewQuota(16)},
		ImplementationDigest: agent.ComputeDigest([]byte("example-greeter")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-greeter-config")),
	}))

	ctx := context.Background()
	engine := exampleValue(agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}))
	defer func() {
		if err := engine.Close(ctx); err != nil {
			panic(err)
		}
	}()
	process := exampleValue(engine.Start(ctx, greeter, exampleValue(agent.EncodePayload(greetRequest{Name: "ada"}))))
	result := exampleValue(process.Await(ctx))
	output, _ := result.Termination().Output()
	fmt.Println(exampleValue(output.Decode[greeting]()).Message)
	// Output:
	// hi ada
}

type greetRequest struct {
	Name string `json:"name"`
}

type greeting struct {
	Message string `json:"message"`
}

type greetModel struct{}

func (greetModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	var req greetRequest
	if err := jsonv2.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Text()), &req); err != nil || req.Name == "" {
		return nil, errors.New("greet model received an invalid request")
	}
	data, err := jsonv2.Marshal(greeting{Message: "hi " + req.Name})
	if err != nil {
		return nil, err
	}
	message := chat.NewAssistantMessage(chat.NewTextPart(string(data)))
	return chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, nil)
}

func greetDeployment() agent.Deployment {
	client := exampleValue(chatclient.New(greetModel{}, chatclient.Config{}))
	definition := exampleValue(interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "example.greet", Description: "Produce a greeting from the request.", MaxModelCalls: agent.NewQuota(1),
	}))
	dispatcher := exampleValue(interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: client}))
	return exampleValue(agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("example-greet")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-greet-config")),
	}))
}

func exampleValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
