package mcp

import (
	"context"
	"errors"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
)

// ErrNoServerSession reports a reverse call made outside an MCP tool invocation.
var ErrNoServerSession = errors.New("mcp: no active MCP server session on context")

type serverCall struct {
	session       *sdkmcp.ServerSession
	progressToken any
}

type serverCallKey struct{}

func withServerCall(ctx context.Context, request *sdkmcp.CallToolRequest) context.Context {
	if request == nil || request.Session == nil {
		return ctx
	}
	call := serverCall{session: request.Session}
	if request.Params != nil {
		call.progressToken = request.Params.GetProgressToken()
	}
	return context.WithValue(ctx, serverCallKey{}, call)
}

func serverCallFromContext(ctx context.Context) serverCall {
	if ctx == nil {
		return serverCall{}
	}
	call, _ := ctx.Value(serverCallKey{}).(serverCall)
	return call
}

// ReportProgress sends a progress notification back to the client.
// progress should increase monotonically; total is optional and may
// be left nil when the work size is unknown. message is a free-form
// human-readable status string.
//
// The originating client must have included a progressToken in its
// tools/call request — otherwise this helper returns nil without
// sending a notification (the spec mandates that servers only emit
// progress when explicitly opted in). Errors propagate from the
// underlying [*sdkmcp.ServerSession.NotifyProgress].
//
// Example:
//
//	func (t *longTool) Call(ctx context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
//	    for i := range 100 {
//	        // ... work ...
//	        _ = mcp.ReportProgress(ctx, float64(i+1), new(100.0),
//	            fmt.Sprintf("processed %d/100", i+1))
//	    }
//	    return chat.NewTextToolOutput("done"), nil
//	}
func ReportProgress(ctx context.Context, progress float64, total *float64, message string) error {
	return serverCallFromContext(ctx).reportProgress(ctx, progress, total, message)
}

func (s serverCall) reportProgress(ctx context.Context, progress float64, total *float64, message string) error {
	if s.session == nil {
		return ErrNoServerSession
	}
	if s.progressToken == nil {
		// Client did not opt in; per spec the handler stays silent.
		return nil
	}

	params := &sdkmcp.ProgressNotificationParams{
		ProgressToken: s.progressToken,
		Progress:      progress,
		Message:       message,
	}
	if total != nil {
		params.Total = *total
	}
	return s.session.NotifyProgress(ctx, params)
}

// Elicit asks the connected client to surface a structured
// prompt to the end user and returns their response. Useful when a
// tool needs runtime clarification it could not have asked for at
// schema-design time (auth confirmation, ambiguous filename, ...).
//
// Returns [ErrNoServerSession] when called outside an MCP dispatch.
// Underlying RPC errors propagate as-is.
//
// Example — structured response:
//
//	res, err := mcp.Elicit(ctx, sdkmcp.ElicitParams{
//	    Message: "Choose a deployment target",
//	    RequestedSchema: map[string]any{
//	        "type": "object",
//	        "properties": map[string]any{
//	            "env": map[string]any{
//	                "type": "string",
//	                "enum": []string{"staging", "prod"},
//	            },
//	        },
//	        "required": []string{"env"},
//	    },
//	})
//	if err != nil { return "", err }
//	if res.Action != "accept" { return "user canceled", nil }
//	env, _ := res.Content["env"].(string)
func Elicit(ctx context.Context, params sdkmcp.ElicitParams) (*sdkmcp.ElicitResult, error) {
	return serverCallFromContext(ctx).elicit(ctx, params)
}

func (s serverCall) elicit(ctx context.Context, params sdkmcp.ElicitParams) (*sdkmcp.ElicitResult, error) {
	if s.session == nil {
		return nil, ErrNoServerSession
	}
	return s.session.Elicit(ctx, new(params))
}
