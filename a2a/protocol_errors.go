package a2a

import (
	"context"
	"errors"
	"strings"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/otel/errortelemetry"
)

// SDK Error messages are an explicit public contract; ordinary store and handler
// diagnostics are private even when the SDK can serialize their text.
type protocolErrors struct{}

func (protocolErrors) Before(ctx context.Context, _ *a2asrv.CallContext, _ *a2asrv.Request) (context.Context, any, error) {
	return ctx, nil, nil
}

func (protocolErrors) After(ctx context.Context, _ *a2asrv.CallContext, response *a2asrv.Response) error {
	if response.Err == nil {
		return nil
	}
	if public, found := errors.AsType[*sdka2a.Error](response.Err); found && public.Message != "" {
		response.Err = public
		return nil
	}
	errortelemetry.Record(trace.SpanFromContext(ctx), errortelemetry.Classify(response.Err))
	reason := sdka2a.ErrorReason(response.Err)
	response.Payload = nil
	if reason == sdka2a.ErrorReason(sdka2a.ErrInternalError) {
		response.Err = sdka2a.ErrInternalError
	} else {
		// The SDK still selects the native code from the cause, while its public
		// reason cannot expose a private wrapper or unpublished error details.
		response.Err = sdka2a.NewError(response.Err, strings.ToLower(strings.ReplaceAll(reason, "_", " ")))
	}
	return nil
}
