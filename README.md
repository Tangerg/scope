# Scope

Scope is a modular Go foundation for building AI systems. It provides portable contracts and composable infrastructure for models, agents, data pipelines, retrieval, evaluation, tools, interoperability, and observability.

## Position

Scope is a framework and library. Applications select the modules they need and compose them through explicit Go contracts. Provider and storage integrations remain optional, independently versioned leaves.

## Boundaries

Scope owns reusable AI infrastructure semantics. It does not own product sessions, dashboards, user identity, billing, marketplaces, deployment catalogs, application persistence, or desktop workflows. Those belong to the host application, such as Flame.

Each capability has one owning package and one public representation. Scope avoids root façades, hidden registration, ambient authority, and provider details in shared contracts.

## Choose an entry

| Need | Canonical APIs | Checked source |
| --- | --- | --- |
| Ordinary structured model calls | `chatclient.New`, `Client.Output`; streaming uses `NewStreamClient` | [Core-only consumer](examples/chat/main.go) |
| Managed execution | Interaction `Definition` and `Dispatcher`, `Deployment`, `Engine.Start`, `Process.Await` | [Direct versus managed](examples/agent/direct_vs_managed/main.go) |
| Recovery and remote reconciliation | `TreeCommitter`, `Engine.RestoreTree`, `ToolSet.SettleToolResult`, `Process.ResolveUnknownEffect` | [Unknown outcomes](examples/agent/reconciliation/doc.go), [PostgreSQL and process crashes](dev/treecommitterconformance/doc.go) |

Start with ordinary calls when you only need model I/O. The first consumer and
its tests depend on Core alone. Managed execution adds an explicit commit
boundary; `MemoryTreeCommitter` retains facts only for that instance's lifetime.
Persistent storage and business transactions stay with the Host. A terminal
Process status does not establish that every remote operation has a known result.

## Ordinary structured call

This offline example prints `42` using a deterministic model. It demonstrates the
calling contract, not live-model quality. The code below is checked against
[its runnable source](examples/chat/main.go). To use a real model, supply a
provider's `chat.Model`, such as [OpenAI](models/openai/doc.go).

```go
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
```

## Run and verify the consumers

From the repository root, run the same examples against workspace sources:

```sh
go run ./examples/chat
go run ./examples/agent/direct_vs_managed
go run ./examples/agent/reconciliation
```

The dependency versions have one declaration in [examples/go.mod](examples/go.mod).
The [workspace](go.work) selects the toolchain. To inspect the published version
combination and run without workspace sources, change into `examples`:

```sh
GOWORK=off go list -m github.com/Tangerg/scope/core github.com/Tangerg/scope/agent
GOWORK=off go run ./chat
GOWORK=off go run ./agent/direct_vs_managed
GOWORK=off go run ./agent/reconciliation
```

The examples module has no local replacements. From the repository root,
`MODULE=examples scripts/check.sh isolate` checks module hygiene and every
consumer's behavior against those published dependencies. The full default
`scripts/check.sh` also checks workspace behavior, race detection, integration
compilation, benchmarks, lint, statement-coverage budgets, and reachable vulnerabilities.

The offline recovery example uses memory storage and a simulated remote ledger.
For real PostgreSQL transactions and independent-process failure, run
`scripts/check-tree-committer.sh` with Docker available. It provisions an isolated
container and runs the same conformance and crash tests with both workspace and
published dependencies; no credentials or shared database are required.

## Evolution

Modules follow semantic versioning independently. Before version 1, a minor release may replace a wrong public design without retaining aliases or compatibility shims. Consumers should pin the module versions they adopt and review minor upgrades.

## License

Scope is licensed under the [Apache License 2.0](LICENSE).
