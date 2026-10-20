package history_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/inmemory"
)

func Example() {
	conversationID, err := history.NewConversationID("customer-42")
	if err != nil {
		panic(err)
	}
	fmt.Println(conversationID)
	// Output: customer-42
}

func ExampleNewMiddleware() {
	store := new(inmemory.Store)
	middleware, err := history.NewMiddleware(history.MiddlewareConfig{Store: store, ConversationID: "customer-42"})
	if err != nil {
		panic(err)
	}
	model := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("Hello"))), FinishReason: chat.FinishReasonStop}}, nil
	}))
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("Hi")))
	if err != nil {
		panic(err)
	}
	response, err := model.Call(context.Background(), request)
	if err != nil {
		panic(err)
	}
	messages, err := store.Read(context.Background(), "customer-42")
	if err != nil {
		panic(err)
	}
	fmt.Println(response.Text(), len(messages))
	// Output: Hello 2
}
