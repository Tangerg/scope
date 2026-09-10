package chatclient_test

import (
	"context"
	"fmt"
	"iter"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func Example() {
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("Hello from the model"), nil
	})
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		panic(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("Hello")))
	if err != nil {
		panic(err)
	}
	request.Options.Model = "example"
	response, err := client.Call(context.Background(), request)
	if err != nil {
		panic(err)
	}
	fmt.Println(response.Text())
	// Output: Hello from the model
}

func ExampleStreamClient_Stream() {
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if !yield(delta("Hello ", ""), nil) {
				return
			}
			yield(delta("stream", chat.FinishReasonStop), nil)
		}
	})
	client, err := chatclient.NewStreamClient(streamer, chatclient.StreamConfig{})
	if err != nil {
		panic(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("Hello")))
	if err != nil {
		panic(err)
	}
	for response, streamErr := range client.Stream(context.Background(), request) {
		if streamErr != nil {
			panic(streamErr)
		}
		fmt.Print(response.Text())
	}
	fmt.Println()
	// Output: Hello stream
}

func ExampleTemplate() {
	prompt, err := chatclient.ParseTemplate("Explain {{.Topic}} in one sentence.")
	if err != nil {
		panic(err)
	}
	message, err := prompt.UserMessage(struct{ Topic string }{Topic: "Go interfaces"})
	if err != nil {
		panic(err)
	}
	fmt.Println(message.Text())
	// Output: Explain Go interfaces in one sentence.
}

func ExampleClient_Output() {
	type answer struct {
		Value int `json:"value"`
	}
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse(`{"value":42}`), nil
	})
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		panic(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("What is six times seven?")))
	if err != nil {
		panic(err)
	}
	result, err := client.Output(context.Background(), request, chatclient.JSON[answer]())
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Value)
	// Output: 42
}

func textResponse(text string) *chat.Response {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return &chat.Response{Output: &chat.Output{
		Message:      &message,
		FinishReason: chat.FinishReasonStop,
	}}
}

func delta(text string, reason chat.FinishReason) *chat.ResponseDelta {
	return &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(text)}, FinishReason: reason}
}
