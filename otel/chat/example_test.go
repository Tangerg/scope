package chat_test

import (
	"context"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	otelchat "github.com/Tangerg/scope/otel/chat"
)

func ExampleMiddleware_Call() {
	middleware, err := otelchat.NewMiddleware(otelchat.MiddlewareConfig{Provider: "example"})
	if err != nil {
		panic(err)
	}
	model := middleware.Call(corechat.ModelFunc(func(context.Context, *corechat.Request) (*corechat.Response, error) {
		message := corechat.NewAssistantMessage(corechat.NewTextPart("instrumented"))
		output, outputErr := corechat.NewOutput(&message, corechat.FinishReasonStop)
		if outputErr != nil {
			return nil, outputErr
		}
		return corechat.NewResponse(output, nil)
	}))
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		panic(err)
	}
	response, err := model.Call(context.Background(), request)
	if err != nil {
		panic(err)
	}

	fmt.Println(response.Text())
	// Output:
	// instrumented
}
