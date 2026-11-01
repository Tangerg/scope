package agent_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func ExampleAdapt() {
	ctx := context.Background()
	format := chatclient.JSON[greeting]()
	greeter := exampleValue(agent.Adapt(ctx, agent.AdapterConfig[greetRequest, greeting]{
		Name: "example.greeter", Description: "Greet a caller by name.",
		Interaction: greetDeployment(),
		BuildInput: func(_ context.Context, req greetRequest) (agent.Input, error) {
			data, err := jsonv2.Marshal(req)
			if err != nil {
				return agent.Input{}, err
			}
			return agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(data)))}}, nil
		},
		DecodeOutput: func(_ context.Context, output agent.Output) (greeting, error) {
			return format.Decode(output.ModelResponse)
		},
		Budget:               run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(16), Signals: run.NewQuota(16)},
		ImplementationDigest: run.ComputeDigest([]byte("example-greeter")),
		ConfigurationDigest:  run.ComputeDigest([]byte("example-greeter-config")),
	}))

	engine := exampleValue(run.New(context.Background(), run.Config{Store: run.NewMemoryStore()}))
	defer func() {
		if err := engine.Close(ctx); err != nil {
			panic(err)
		}
	}()
	output := exampleValue(engine.Run(ctx, greeter, greetRequest{Name: "ada"}))
	fmt.Println(output.Message)
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

func greetDeployment() run.Program[agent.Input, agent.Output] {
	client := exampleValue(chatclient.New(greetModel{}, chatclient.Config{}))
	definition := exampleValue(agent.NewDefinition(agent.DefinitionConfig{
		Name: "example.greet", Description: "Produce a greeting from the request.", MaxModelCalls: run.NewQuota(1),
	}))
	dispatcher := exampleValue(agent.NewDispatcher(definition, agent.DispatcherConfig{Model: client}))
	return exampleValue(run.Bind[agent.Input, agent.Output](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("example-greet")),
		ConfigurationDigest:  run.ComputeDigest([]byte("example-greet-config")),
	}))
}

func exampleValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
