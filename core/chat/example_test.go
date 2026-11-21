package chat_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

func Example() {
	request, err := chat.NewRequest(
		chat.NewSystemMessage("Answer concisely."),
		chat.NewUserMessage(chat.NewTextPart("What is a scope?")),
	)
	if err != nil {
		panic(err)
	}
	request.Options = chat.Options{Model: "provider-model"}

	fmt.Println(request.Messages[1].Text())
	fmt.Println(request.Options.Model)
	// Output:
	// What is a scope?
	// provider-model
}

func ExampleWrap() {
	// remindBrevity adds an instruction to each request without changing the
	// caller's messages: the edit is transient, seen only by the next model.
	remindBrevity := func(next chat.Model) chat.Model {
		return chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
			decorated := request.Clone()
			decorated.Messages = append(decorated.Messages, chat.NewSystemMessage("Answer in one sentence."))
			return next.Call(ctx, decorated)
		})
	}
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		message := chat.NewAssistantMessage(chat.NewTextPart(fmt.Sprintf("saw %d messages", len(request.Messages))))
		return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
	})
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("What is a scope?")))
	if err != nil {
		panic(err)
	}
	response, err := chat.Wrap(model, remindBrevity).Call(context.Background(), request)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s; the caller kept %d\n", response.Text(), len(request.Messages))
	// Output: saw 2 messages; the caller kept 1
}
