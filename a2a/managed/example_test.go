package managed_test

import (
	"context"
	"fmt"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a/managed"
	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

func ExampleNewHTTPHandler() {
	ctx := context.Background()
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("Hello"))), FinishReason: chat.FinishReasonStop}}, nil
	})
	program, err := agent.Compile(ctx, agent.Config{Name: "public.greeter", Description: "Answer a greeting", BuildIdentity: run.ComputeDigest([]byte("greeter-build")), ModelIdentity: run.ComputeDigest([]byte("example-model")), Model: model})
	if err != nil {
		panic(err)
	}
	runtime, err := run.New(ctx, run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		panic(err)
	}
	defer runtime.Close(ctx)
	// This endpoint is intentionally shared. A private endpoint must derive its
	// authority from the authenticated principal established by HTTP middleware.
	_, err = managed.NewHTTPHandler(managed.Config{Runtime: runtime, Program: program.Erase(), Service: "greeter", Authority: func(context.Context, string) (string, error) { return "public", nil }, Card: &sdk.AgentCard{Name: "greeter", SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface("https://agent.example/rpc", sdk.TransportProtocolJSONRPC)}}})
	fmt.Println(err)
	// Output: <nil>
}
