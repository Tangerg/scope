// Command direct_vs_managed contrasts a direct model call with the same model
// capability managed as an agent Process. It uses deterministic local models.
// Run go run ./examples/agent/direct_vs_managed from the repository root, or
// GOWORK=off go run ./agent/direct_vs_managed from examples to consume the
// published Agent and Core versions selected by its go.mod.
// MemoryStore retains facts only for that instance's lifetime; this
// example does not demonstrate durable or cross-process recovery. See the
// reconciliation example and runstores/postgres for those boundaries.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Tangerg/scope/agent"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
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

	program, err := agent.Compile(ctx, agent.Config{
		Name: "example.managed_echo", Description: "Return one deterministic echo.",
		Model: echoModel{prefix: "managed"}, MaxModelCalls: agentrun.NewQuota(1),
		BuildIdentity: agentrun.ComputeDigest([]byte("example-managed-echo-implementation")),
		ModelIdentity: agentrun.ComputeDigest([]byte("echo-prefix-managed")),
	})
	if err != nil {
		return err
	}
	engine, err := agentrun.New(ctx, agentrun.Config{Store: agentrun.NewMemoryStore()})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, engine.Close(context.WithoutCancel(ctx))) }()
	process, err := engine.Start(ctx, program, agentrun.StartRequest[agent.Input]{
		ID: agentrun.StartID{Scope: "example", Key: rand.Text()}, Input: agent.Input{Messages: request.Messages},
	})
	if err != nil {
		return err
	}
	result, err := process.Await(ctx)
	if err != nil {
		return err
	}
	if err = engine.ReleaseTree(ctx, process.ID()); err != nil {
		return err
	}
	managed, err := result.Output()
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
