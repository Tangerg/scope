package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/go-sdk/jsonrpc"
	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/tool"
)

func TestToolCallbackPanicRemainsBoundedAtTheSDKBoundary(t *testing.T) {
	const privateDiagnostic = "private-tool-callback-defect"
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: errors.New(privateDiagnostic), Output: tool.NewTextOutput(privateDiagnostic)})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "string", value: privateDiagnostic},
		{name: "error", value: errors.New(privateDiagnostic)},
		{name: "failure is not an observed outcome", value: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			cs, close := connectPair(t, t.Context(), testTool{definition: tool.Definition{Name: "panic", InputSchema: json.RawMessage(`{"type":"object"}`)}, call: func(context.Context, tool.Invocation) (tool.Output, error) { panic(test.value) }})
			defer close()
			result, err := cs.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "panic", Arguments: map[string]any{}})
			protocolError, found := errors.AsType[*jsonrpc.Error](err)
			if result != nil || !found || protocolError.Code != jsonrpc.CodeInternalError || strings.Contains(err.Error(), privateDiagnostic) || len(protocolError.Data) != 0 {
				t.Fatalf("want bounded internal protocol error; got result=%v error=%v", result, err)
			}
		})
	}
}

func TestRegisterAdmitsAllSDKAnnotationsBeforeCatalogMutation(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "admission"}, nil)
	if err := registerTestTools(server, newConstantTool("existing")); err != nil {
		t.Fatal(err)
	}
	invalid := testTool{definition: tool.Definition{Name: "z_bad", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string","x-mcp-header":"bad header"}}}`)}}
	var registrationError error
	var defect any
	func() {
		defer func() { defect = recover() }()
		registrationError = registerTestTools(server, newConstantTool("a_good"), invalid)
	}()
	cs, close := connectServer(t, t.Context(), server)
	defer close()
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if defect != nil || registrationError == nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "existing" {
		t.Fatalf("Register violated atomic error boundary: panic=%v returned_error=%v registered_tool_count=%d", defect, registrationError, len(listed.Tools))
	}
}
