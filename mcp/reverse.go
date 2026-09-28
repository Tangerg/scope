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

// ReportProgress requires a client-supplied progressToken; otherwise it returns
// nil without sending a notification. Progress should increase monotonically.
// A nil total means the work size is unknown. Session errors propagate unchanged.
func ReportProgress(ctx context.Context, progress float64, total *float64, message string) error {
	return serverCallFromContext(ctx).reportProgress(ctx, progress, total, message)
}

func (s serverCall) reportProgress(ctx context.Context, progress float64, total *float64, message string) error {
	if s.session == nil {
		return ErrNoServerSession
	}
	if s.progressToken == nil {

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

// Elicit asks the connected client for structured user input. It returns
// ErrNoServerSession outside MCP dispatch and preserves RPC error causes.
func Elicit(ctx context.Context, params sdkmcp.ElicitParams) (*sdkmcp.ElicitResult, error) {
	return serverCallFromContext(ctx).elicit(ctx, params)
}

func (s serverCall) elicit(ctx context.Context, params sdkmcp.ElicitParams) (*sdkmcp.ElicitResult, error) {
	if s.session == nil {
		return nil, ErrNoServerSession
	}
	return s.session.Elicit(ctx, new(params))
}
