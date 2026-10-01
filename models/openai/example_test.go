package openai_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/openai"
)

func ExampleNewChatCompletions() {
	model, err := openai.NewChatCompletions(context.Background(), openai.ChatCompletionsConfig{
		APIKey:         "example-key",
		DefaultOptions: chat.Options{Model: "example-model"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(model != nil)
	// Output:
	// true
}
