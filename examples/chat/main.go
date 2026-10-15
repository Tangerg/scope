package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func main() {
	if err := run(context.Background(), os.Stdout, arithmeticModel{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type answer struct {
	Value int `json:"value"`
}

func run(ctx context.Context, output io.Writer, model chat.Model) error {
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		return err
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("What is six times seven?")))
	if err != nil {
		return err
	}
	result, err := client.Output(ctx, request, chatclient.JSON[answer]())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, result.Value)
	return err
}

type arithmeticModel struct{}

func (arithmeticModel) Call(ctx context.Context, request *chat.Request) (*chat.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &chat.Response{Output: &chat.Output{
		Message: new(chat.NewAssistantMessage(chat.NewTextPart(`{"value":42}`))), FinishReason: chat.FinishReasonStop,
	}}, nil
}
