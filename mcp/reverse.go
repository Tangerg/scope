package mcp

import (
	"context"
	"errors"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
)

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

// Without a progress token, the client has not requested notifications.
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

func Elicit(ctx context.Context, params sdkmcp.ElicitParams) (*sdkmcp.ElicitResult, error) {
	return serverCallFromContext(ctx).elicit(ctx, params)
}

func (s serverCall) elicit(ctx context.Context, params sdkmcp.ElicitParams) (*sdkmcp.ElicitResult, error) {
	if s.session == nil {
		return nil, ErrNoServerSession
	}
	return s.session.Elicit(ctx, new(params))
}
