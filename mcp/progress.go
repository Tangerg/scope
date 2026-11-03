package mcp

import (
	"context"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

// sessionProgress delivers a served call's progress as MCP notifications under
// the token its client supplied.
type sessionProgress struct {
	notify func(context.Context, *sdkmcp.ProgressNotificationParams) error
	token  any
}

func (s sessionProgress) ReportProgress(ctx context.Context, progress toolcontract.Progress) error {
	return s.notify(ctx, &sdkmcp.ProgressNotificationParams{
		ProgressToken: s.token,
		Progress:      progress.Completed,
		Total:         progress.Total,
		Message:       progress.Message,
	})
}

// requestProgress is nil unless the client asked for progress with a token.
func requestProgress(request *sdkmcp.CallToolRequest) toolcontract.ProgressReporter {
	if request == nil || request.Session == nil || request.Params == nil {
		return nil
	}
	token := request.Params.GetProgressToken()
	if token == nil {
		return nil
	}
	return sessionProgress{notify: request.Session.NotifyProgress, token: token}
}
