package tool_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func ExampleNewInputRequest() {
	schema, err := jsonschema.Parse([]byte(`{"type":"boolean"}`))
	if err != nil {
		panic(err)
	}
	request, err := tool.NewInputRequest(tool.InputRequestConfig{
		Prompt: []byte(`"Approve the write?"`), ResponseSchema: schema, ContinuationState: []byte(`{"record":"draft"}`),
	})
	if err != nil {
		panic(err)
	}
	result := tool.Result{InputRequest: &request}
	continuation, err := result.InputRequest.Continuation([]byte(`true`))
	if err != nil {
		panic(err)
	}
	// The runtime persists the checkpoint and routes this answer to the same
	// call through binding.Contract().Prepare(call, tool.Admission{Continuation: &continuation}).
	fmt.Println(string(continuation.State()), string(continuation.Response()))
	// Output: {"record":"draft"} true
}

func Example() {
	type input struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	add, err := tool.NewFunc(tool.FuncConfig{
		Name:        "add",
		Description: "add two integers",
	}, func(_ context.Context, value input) (int, error) {
		return value.A + value.B, nil
	})
	if err != nil {
		panic(err)
	}
	bound, err := tool.Bind(add)
	if err != nil {
		panic(err)
	}
	registry, err := tool.NewRegistry(bound)
	if err != nil {
		panic(err)
	}

	fmt.Println(registry.Definitions()[0].Name)
	binding, ok := registry.Resolve("add")
	if !ok {
		panic("missing add")
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: "add", Arguments: `{"a":2,"b":3}`}, tool.Admission{})
	if err != nil {
		panic(err)
	}
	result, err := binding.Call(context.Background(), invocation)
	if err != nil {
		panic(err)
	}
	text, _ := result.Output.Text()
	fmt.Println(text)
	// Output:
	// add
	// 5
}

func ExampleNewFailure() {
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Cause: errors.New("internal authorization diagnostic"),
		Output: tool.NewTextOutput("this operation is not permitted"),
	})
	if err != nil {
		panic(err)
	}
	wrapped := fmt.Errorf("tool call: %w", failure)
	result, found := tool.ClassifyFailure(wrapped)
	if !found {
		panic("missing definite outcome")
	}
	text, _ := result.Output().Text()
	fmt.Println(result.Kind(), text)
	fmt.Println(errors.Is(wrapped, result.Cause()))
	// Output:
	// rejected this operation is not permitted
	// false
}

func ExampleNewGuard() {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "inspect"}, func(context.Context, struct{}) (string, error) {
		return "inspected", nil
	})
	if err != nil {
		panic(err)
	}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
			return false, nil
		}),
	})
	if err != nil {
		panic(err)
	}
	binding, err := tool.Bind(tool.Wrap(executable, guard.Call))
	if err != nil {
		panic(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: "inspect", Arguments: `{}`}, tool.Admission{})
	if err != nil {
		panic(err)
	}
	_, err = binding.Call(context.Background(), invocation)
	failure, found := tool.ClassifyFailure(err)
	if !found {
		panic(err)
	}
	fmt.Println(failure.Kind())
	// Output:
	// rejected
}

// printCalls is a CallMiddleware that reports each call before forwarding it.
func printCalls(next tool.Caller) tool.Caller {
	return tool.CallerFunc(func(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
		fmt.Println("calling", invocation.Definition().Name)
		return next.Call(ctx, invocation)
	})
}

func ExampleWrap() {
	type input struct {
		Query string `json:"query"`
	}
	search, err := tool.NewFunc(tool.FuncConfig{Name: "search"}, func(_ context.Context, value input) (string, error) {
		return "results for " + value.Query, nil
	})
	if err != nil {
		panic(err)
	}
	binding, err := tool.Bind(tool.Wrap(search, printCalls))
	if err != nil {
		panic(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: "search", Arguments: `{"query":"scope"}`}, tool.Admission{})
	if err != nil {
		panic(err)
	}
	result, err := binding.Call(context.Background(), invocation)
	if err != nil {
		panic(err)
	}
	text, _ := result.Output.Text()
	fmt.Println(text)
	// Output:
	// calling search
	// results for scope
}

// approvalRequired is a declaration a host defines for its own policy.
type approvalRequired interface{ RequiresApproval() bool }

// approved makes the declaration for a Tool the host does not own. Through
// Unwrap, the declarations of that Tool stay reachable.
type approved struct{ tool.Tool }

func (a approved) Unwrap() tool.Tool { return a.Tool }

func (approved) RequiresApproval() bool { return true }

func ExampleAs() {
	type input struct {
		Path string `json:"path"`
	}
	remove, err := tool.NewFunc(tool.FuncConfig{Name: "remove", Effect: tool.EffectUnsafe}, func(_ context.Context, value input) (string, error) {
		return "removed " + value.Path, nil
	})
	if err != nil {
		panic(err)
	}
	decorated := tool.Wrap(approved{Tool: remove}, printCalls)

	declaration, declared := tool.As[approvalRequired](decorated)
	_, validates := tool.As[tool.InputValidatingTool](decorated)
	fmt.Println(declared && declaration.RequiresApproval(), validates)
	// Output: true true
}
