// The independent peer uses the MCP SDK without importing Scope.
package main

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"sync/atomic"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
)

const (
	statusURI          = "expert://status"
	disconnectQuery    = "disconnect"
	waitQuery          = "wait"
	disconnectExitCode = 23
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var calls, canceled atomic.Int64
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "independent-research-expert"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name: "search", Description: "Search the offline reference corpus.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
	}, func(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var input struct {
			Query string `json:"query"`
		}
		if err := jsonv2.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}
		calls.Add(1)
		if _, err := fmt.Fprintln(os.Stderr, "search:started"); err != nil {
			return nil, err
		}
		switch input.Query {
		case disconnectQuery:
			// Exit after accepting the request, before delivering its result.
			os.Exit(disconnectExitCode)
		case waitQuery:
			<-ctx.Done()
			canceled.Add(1)
			if _, err := fmt.Fprintln(os.Stderr, "search:canceled"); err != nil {
				return nil, err
			}
			return nil, ctx.Err()
		}
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{
			Text: `[{"url":"https://example.invalid/independent-expert","title":"Independent source"}]`,
		}}}, nil
	})
	server.AddPrompt(&sdkmcp.Prompt{Name: "researcher_role", Arguments: []*sdkmcp.PromptArgument{{Name: "topic", Required: true}}},
		func(_ context.Context, request *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
			return &sdkmcp.GetPromptResult{Messages: []*sdkmcp.PromptMessage{{Role: sdkmcp.Role("assistant"),
				Content: &sdkmcp.TextContent{Text: "Independent research for " + request.Params.Arguments["topic"]},
			}}}, nil
		})
	server.AddResource(&sdkmcp.Resource{URI: statusURI, Name: "status", MIMEType: "application/json"},
		func(context.Context, *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
			data, err := jsonv2.Marshal(struct {
				PID      int   `json:"pid"`
				Calls    int64 `json:"calls"`
				Canceled int64 `json:"canceled"`
			}{PID: os.Getpid(), Calls: calls.Load(), Canceled: canceled.Load()})
			if err != nil {
				return nil, err
			}
			return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: statusURI, MIMEType: "application/json", Text: string(data)}}}, nil
		})
	return server.Run(context.Background(), &sdkmcp.StdioTransport{})
}
