package inmemory_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/inmemory"
)

func Example() {
	ctx := context.Background()
	store := new(inmemory.Store)
	message := chat.NewUserMessage(chat.NewTextPart("hello"))
	if _, err := store.Write(ctx, history.ConversationID("demo"), message); err != nil {
		panic(err)
	}
	messages, err := store.Read(ctx, history.ConversationID("demo"))
	if err != nil {
		panic(err)
	}
	fmt.Println(len(messages), messages[0].Text())
	// Output: 1 hello
}
