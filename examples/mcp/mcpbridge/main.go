package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

type echoInput struct {
	Text string `json:"text" jsonschema:"required"`
}

const echoToolName = "echo"

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) (err error) {
	echo, err := tool.NewFunc[echoInput, string](
		tool.FuncConfig{
			Name:        echoToolName,
			Description: "echo the input text",
		},
		func(_ context.Context, p echoInput) (string, error) { return p.Text, nil },
	)
	if err != nil {
		return fmt.Errorf("create echo tool: %w", err)
	}

	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "scope-bridge"},
		nil,
	)
	if registerErr := scopemcp.Register(server, echo); registerErr != nil {
		return fmt.Errorf("register echo tool: %w", registerErr)
	}

	// In-memory transports keep the example executable without hiding the
	// production boundary: a deployed server would use StdioTransport here.
	//
	//	server.Run(ctx, &sdkmcp.StdioTransport{})
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return fmt.Errorf("connect MCP server: %w", err)
	}
	defer func() {
		if closeErr := serverSession.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close MCP server session: %w", closeErr))
		}
	}()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "demo-host"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return fmt.Errorf("connect MCP client: %w", err)
	}
	defer func() {
		if closeErr := clientSession.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close MCP client session: %w", closeErr))
		}
	}()

	for descriptor, err := range clientSession.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("list MCP tools: %w", err)
		}
		fmt.Printf("[host] discovered tool: %s — %s\n", descriptor.Name, descriptor.Description)
	}

	result, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      echoToolName,
		Arguments: json.RawMessage(`{"text":"hello from the host"}`),
	})
	if err != nil {
		return fmt.Errorf("call MCP tool %q: %w", echoToolName, err)
	}
	if len(result.Content) != 1 {
		return fmt.Errorf("MCP tool %q returned %d content items, want 1", echoToolName, len(result.Content))
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		return fmt.Errorf("MCP tool %q returned content type %T, want text", echoToolName, result.Content[0])
	}
	fmt.Printf("[host] tool result: %s\n", text.Text)
	return nil
}
