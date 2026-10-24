package role_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/role"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

type greetRequest struct {
	Name string `json:"name"`
}

type greeting struct {
	Message string `json:"message"`
}

type greetModel struct{}

func (greetModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	var req greetRequest
	last := request.Messages[len(request.Messages)-1].Text()
	if err := jsonv2.Unmarshal([]byte(last), &req); err != nil || req.Name == "" {
		return nil, errors.New("greet model received an invalid request")
	}
	data, err := jsonv2.Marshal(greeting{Message: "hi " + req.Name})
	if err != nil {
		return nil, err
	}
	message := chat.NewAssistantMessage(chat.NewTextPart(string(data)))
	return chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, nil)
}

func greetInteraction(t *testing.T) agent.Deployment {
	t.Helper()
	client, err := chatclient.New(greetModel{}, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "role.test.greet", Description: "Produce a greeting from the request.", MaxModelCalls: agent.NewQuota(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: client})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("greet-impl")), ConfigurationDigest: agent.ComputeDigest([]byte("greet-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func TestRoleRunsBusinessTypedInteraction(t *testing.T) {
	format := chatclient.JSON[greeting]()
	deployment, err := role.New(role.Config[greetRequest, greeting]{
		Name: "role.test.greeter", Description: "Greet a caller by name.",
		Interaction: greetInteraction(t),
		BuildInput: func(_ context.Context, req greetRequest) (interaction.Input, error) {
			data, marshalErr := jsonv2.Marshal(req)
			if marshalErr != nil {
				return interaction.Input{}, marshalErr
			}
			return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(data)))}}, nil
		},
		DecodeOutput: func(_ context.Context, output interaction.Output) (greeting, error) {
			if output.ModelResponse == nil {
				return greeting{}, errors.New("role produced no model response")
			}
			return format.Decode(output.ModelResponse)
		},
		Budget:               agent.Budget{Steps: agent.NewQuota(16), Effects: agent.NewQuota(16), Signals: agent.NewQuota(16)},
		ImplementationDigest: agent.ComputeDigest([]byte("greeter-impl")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("greeter-config")),
	})
	if err != nil {
		t.Fatal(err)
	}

	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Close(context.WithoutCancel(t.Context())) }()
	input, err := agent.EncodePayload(greetRequest{Name: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := process.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Termination().Status() != agent.StatusCompleted {
		t.Fatalf("status = %s", result.Termination().Status())
	}
	output, present := result.Termination().Output()
	if !present {
		t.Fatal("completed role has no output")
	}
	got, err := output.Decode[greeting]()
	if err != nil {
		t.Fatal(err)
	}
	if got.Message != "hi ada" {
		t.Fatalf("greeting = %q, want %q", got.Message, "hi ada")
	}
}

func TestRoleConfigGuards(t *testing.T) {
	deployment := greetInteraction(t)
	build := func(context.Context, greetRequest) (interaction.Input, error) { return interaction.Input{}, nil }
	decode := func(context.Context, interaction.Output) (greeting, error) { return greeting{}, nil }

	if _, err := role.New(role.Config[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", Interaction: deployment, DecodeOutput: decode,
	}); !errors.Is(err, role.ErrInvalidConfig) {
		t.Fatalf("missing BuildInput error = %v, want ErrInvalidConfig", err)
	}
	if _, err := role.New(role.Config[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", Interaction: deployment, BuildInput: build,
	}); !errors.Is(err, role.ErrInvalidConfig) {
		t.Fatalf("missing DecodeOutput error = %v, want ErrInvalidConfig", err)
	}
	if _, err := role.New(role.Config[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", BuildInput: build, DecodeOutput: decode,
	}); !errors.Is(err, role.ErrInvalidConfig) {
		t.Fatalf("missing Interaction error = %v, want ErrInvalidConfig", err)
	}
}
