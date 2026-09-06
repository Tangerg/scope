// Command direct_vs_managed contrasts a direct model call with the same model
// capability managed as a recoverable agent Process.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	directClient, err := chatclient.New(echoModel{prefix: "direct"}, chatclient.Config{})
	if err != nil {
		return err
	}
	request := &chat.Request{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("hello")),
	}}
	direct, err := directClient.Call(ctx, request)
	if err != nil {
		return err
	}

	managedClient, err := chatclient.New(echoModel{prefix: "managed"}, chatclient.Config{})
	if err != nil {
		return err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name:          "example.managed_echo",
		Description:   "Return one deterministic model response through an Engine Process.",
		MaxModelCalls: 1,
	})
	if err != nil {
		return err
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Client: managedClient})
	if err != nil {
		return err
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		Dispatcher:           dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("example-managed-echo-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-managed-echo-configuration")),
	})
	if err != nil {
		return err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		return err
	}
	defer engine.Close()
	input, err := deployment.Descriptor().EncodeInput(interaction.Input{Messages: request.Messages})
	if err != nil {
		return err
	}
	process, err := engine.Start(ctx, deployment, input)
	if err != nil {
		return err
	}
	result, err := process.Await(ctx)
	if err != nil {
		return err
	}
	if releaseErr := engine.ReleaseTree(ctx, process.ID()); releaseErr != nil {
		return releaseErr
	}
	erased, ok := result.Output()
	if !ok {
		return fmt.Errorf("managed Process ended with %s", result.Status())
	}
	managed, err := deployment.Descriptor().DecodeOutput[interaction.Output](erased)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "direct: %s\nmanaged: %s\n", direct.Text(), managed.ModelResponse.Text())
	return err
}

type echoModel struct{ prefix string }

func (e echoModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	last := request.Messages[len(request.Messages)-1]
	message := chat.NewAssistantMessage(chat.NewTextPart(e.prefix + " " + last.Text()))
	return &chat.Response{Output: &chat.Output{
		Message: &message, FinishReason: chat.FinishReasonStop,
	}}, nil
}
