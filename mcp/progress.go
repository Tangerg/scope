package mcp

import (
	"context"
	"errors"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
)

var ErrNoProgressReporter = errors.New("mcp: no MCP progress reporter on context")

type progressReporter struct {
	notify        func(context.Context, *sdkmcp.ProgressNotificationParams) error
	progressToken any
}

type progressReporterKey struct{}

func withProgressReporter(ctx context.Context, request *sdkmcp.CallToolRequest) context.Context {
	if request == nil || request.Session == nil {
		return ctx
	}
	call := progressReporter{notify: request.Session.NotifyProgress}
	if request.Params != nil {
		call.progressToken = request.Params.GetProgressToken()
	}
	return context.WithValue(ctx, progressReporterKey{}, call)
}

func progressReporterFromContext(ctx context.Context) progressReporter {
	if ctx == nil {
		return progressReporter{}
	}
	call, _ := ctx.Value(progressReporterKey{}).(progressReporter)
	return call
}

// Without a progress token, the client has not requested notifications.
func ReportProgress(ctx context.Context, progress float64, total *float64, message string) error {
	return progressReporterFromContext(ctx).reportProgress(ctx, progress, total, message)
}

func (p progressReporter) reportProgress(ctx context.Context, progress float64, total *float64, message string) error {
	if p.notify == nil {
		return ErrNoProgressReporter
	}
	if p.progressToken == nil {
		return nil
	}
	params := &sdkmcp.ProgressNotificationParams{
		ProgressToken: p.progressToken,
		Progress:      progress,
		Message:       message,
	}
	if total != nil {
		params.Total = *total
	}
	return p.notify(ctx, params)
}
