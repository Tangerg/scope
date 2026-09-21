package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestToolDetailsAcrossTransport(t *testing.T) {
	for _, payload := range []string{"", "null", "9007199254740993", `{"n":18446744073709551615}`, `[null,9007199254740993]`, "false", `"text"`} {
		t.Run(payload, func(t *testing.T) {
			serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
			server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "server", Version: "1"}, nil)
			server.AddTool(&sdkmcp.Tool{Name: "result", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				result := &sdkmcp.CallToolResult{}
				if payload != "" {
					result.StructuredContent = json.RawMessage(payload)
				}
				return result, nil
			})
			serverSession, err := server.Connect(t.Context(), serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer serverSession.Close()
			client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "client", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Name: "source", Session: session}}, scopemcp.ToolDiscoveryConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if len(tools) != 1 {
				t.Fatalf("tools = %d", len(tools))
			}
			output, err := invokeTestTool(t.Context(), tools[0], "{}")
			if err != nil {
				t.Fatal(err)
			}

			if payload == "" {
				if len(output.Details) != 0 {
					t.Fatalf("omitted content = %#v", output.Details)
				}
				return
			}
			if len(output.Details) == 0 {
				t.Fatal("present content became absent")
			}
			encoded := output.Details
			if string(encoded) != payload {
				t.Fatalf("structured content = %s; want %s", encoded, payload)
			}
		})
	}
}
