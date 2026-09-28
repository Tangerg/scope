package core_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

type echoModel struct{}

func (echoModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	message := chat.NewAssistantMessage(chat.NewTextPart(request.Messages[0].Text()))
	output, err := chat.NewOutput(&message, chat.FinishReasonStop, nil)
	if err != nil {
		return nil, err
	}
	return chat.NewResponse(output, nil)
}

func Example() {
	client, err := chatclient.New(echoModel{}, chatclient.Config{})
	if err != nil {
		panic(err)
	}

	request := &chat.Request{
		Messages: []chat.Message{
			chat.NewUserMessage(chat.NewTextPart("hello")),
		},
		Options: chat.Options{Model: "example-model"},
	}
	response, err := client.Call(context.Background(), request)
	if err != nil {
		panic(err)
	}

	fmt.Println(response.Text())
	// Output:
	// hello
}
