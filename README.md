# Scope

Scope is a modular Go foundation for building AI systems. It provides portable contracts and composable infrastructure for models, agents, data pipelines, retrieval, evaluation, tools, interoperability, and observability.

## Position

Scope is a framework and library. Applications select the modules they need and compose them through explicit Go contracts. Provider and storage integrations remain optional, independently versioned leaves.

## Boundaries

Scope owns reusable AI infrastructure semantics. It does not own product sessions, dashboards, user identity, billing, marketplaces, deployment catalogs, application persistence, or desktop workflows. Those belong to the host application, such as Flame.

Each capability has one owning package and one public representation. Scope avoids root façades, hidden registration, ambient authority, and provider details in shared contracts.

## Direct chat

Use `chatclient` for an ordinary model call; Agent is only needed when the host
requires managed execution, planning, or durable lifecycle semantics.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/models/openai"
)

func main() {
	model, err := openai.NewChatCompletions(context.Background(), openai.ChatCompletionsConfig{
		APIKey:         os.Getenv("OPENAI_API_KEY"),
		DefaultOptions: chat.Options{Model: os.Getenv("OPENAI_MODEL")},
	})
	if err != nil {
		log.Fatal(err)
	}
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		log.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("What is Scope?")))
	if err != nil {
		log.Fatal(err)
	}
	response, err := client.Call(context.Background(), request)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(response.Text())
}
```

Install only the modules used by the program:

```sh
go get github.com/Tangerg/scope/core@latest github.com/Tangerg/scope/models/openai@latest
```

## Evolution

Modules follow semantic versioning independently. Before version 1, a minor release may replace a wrong public design without retaining aliases or compatibility shims. Consumers should pin the module versions they adopt and review minor upgrades.

## License

Scope is licensed under the [Apache License 2.0](LICENSE).
