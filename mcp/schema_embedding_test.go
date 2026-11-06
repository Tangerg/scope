package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/jsonschema"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestFormAllowsSchemaKeywordAsPropertyName(t *testing.T) {
	for _, name := range []string{"id", "$id", "$ref", "$dynamicRef", "$schema"} {
		t.Run(name, func(t *testing.T) {
			st, ct := sdkmcp.NewInMemoryTransports()
			server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "keyword-field"}, nil)
			schema := json.RawMessage(fmt.Sprintf(`{"type":"object","required":[%q],"properties":{%q:{"type":"string"}}}`, name, name))
			parsed, parseErr := jsonschema.Parse(schema)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if err := parsed.Validate([]byte(fmt.Sprintf(`{%q:"value"}`, name))); err != nil {
				t.Fatal(err)
			}
			t.Logf("valid JSON Schema and instance confirmed for field %q", name)
			server.AddTool(&sdkmcp.Tool{Name: "form", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				return &sdkmcp.CallToolResult{RequestState: "form-1", InputRequests: sdkmcp.InputRequestMap{"answer": &sdkmcp.ElicitParams{Mode: "form", Message: "Value?", RequestedSchema: schema}}}, nil
			})
			ss, err := server.Connect(t.Context(), st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "schema-client"}, &sdkmcp.ClientOptions{MultiRoundTrip: &sdkmcp.MultiRoundTripOptions{Disabled: true}})
			cs, err := client.Connect(t.Context(), ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			candidates, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: cs}}, scopemcp.ToolDiscoveryConfig{})
			if err != nil {
				t.Fatal(err)
			}
			bound, inv, err := prepareTestTool(candidates[0], "{}")
			if err != nil {
				t.Fatal(err)
			}
			result, err := bound.Call(t.Context(), inv)
			if err != nil {
				t.Fatalf("valid form field %q was mistaken for a schema relocation: %v", name, err)
			}
			if result.InputRequest == nil {
				t.Fatal("form input request missing")
			}
			response := json.RawMessage(fmt.Sprintf(`{"action":"accept","content":{%q:"value"}}`, name))
			if _, err = result.InputRequest.Continuation(response); err != nil {
				t.Fatal(err)
			}
		})
	}
}
