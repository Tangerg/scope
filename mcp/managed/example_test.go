package managed_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/mcp/managed"
)

func ExampleRegister() {
	ctx := context.Background()
	rt := must(run.New(ctx, run.Config{Build: "order-example", Store: run.NewMemoryStore()}))
	defer rt.Close(ctx)
	type order struct {
		ID string `json:"id"`
	}
	stage := must(workflow.Transform("lookup", func(_ context.Context, in order) (string, error) { return in.ID, nil }))
	program := must(workflow.Compile[order, string](rt.Compiler(), workflow.Config{Name: "example.order", Description: "Return an order ID.", Stages: []workflow.Stage{stage}, Revision: "lookup.v1"}))
	server := sdk.NewServer(&sdk.Implementation{Name: "orders", Version: "1"}, nil)
	// The transport's authenticated middleware supplies the grant on every
	// round. Use the same protected secret across replicas of a real service.
	type verifiedGrantKey struct{}
	var key [32]byte
	_, _ = rand.Read(key[:])
	err := managed.Register(server, managed.Config[order, string]{Runtime: rt, Program: program, ResumeKey: key, TokenLifetime: 15 * time.Minute,
		Authorize: func(ctx context.Context, _ *sdk.CallToolRequest) (managed.Grant, error) {
			grant, ok := ctx.Value(verifiedGrantKey{}).(managed.Grant)
			if !ok {
				return managed.Grant{}, errors.New("missing verified grant")
			}
			return grant, nil
		},
		Project: func(id string) (tool.Output, error) { return tool.NewTextOutput(id), nil }})
	fmt.Println(err == nil)
	// Output: true
}
