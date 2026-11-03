package chatclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
)

var (
	ErrNilModel      = errors.New("chatclient: nil model")
	ErrInvalidClient = errors.New("chatclient: uninitialized client")
)

// Client validates and snapshots every request before model is called and
// decodes typed output. Compose middleware with chat.Wrap before New. It is
// as safe for concurrent use as the underlying model. Streaming has its own
// construction boundary in [StreamClient].
type Client struct {
	model chat.Model
}

func New(model chat.Model) (Client, error) {
	if lo.IsNil(model) {
		return Client{}, ErrNilModel
	}
	return Client{model: model}, nil
}

// Output asks the provider to enforce format, then strictly decodes naturally
// completed text. Refusal and other non-successful completion reasons return
// OutputCompletionError. Media and tool requests cannot become typed values.
// Output never repairs JSON or injects format instructions into the prompt.
func (c Client) Output[T any](ctx context.Context, req *chat.Request, format Format[T]) (T, error) {
	var zero T
	if !c.valid() {
		return zero, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: ErrInvalidClient}
	}
	if err := format.validate(); err != nil {
		return zero, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	contract := format.Contract()
	response, err := c.call(ctx, req, &contract)
	return format.decodeResponse(response, err)
}

// Call snapshots and validates req before the middleware and model boundary.
func (c Client) Call(ctx context.Context, req *chat.Request) (*chat.Response, error) {
	if !c.valid() {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: ErrInvalidClient}
	}
	return c.call(ctx, req, nil)
}

func (c Client) call(ctx context.Context, req *chat.Request, format *chat.OutputFormat) (*chat.Response, error) {
	prepared, err := prepareRequest(req, format)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	return c.model.Call(ctx, prepared)
}

func prepareRequest(request *chat.Request, outputFormat *chat.OutputFormat) (*chat.Request, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: nil request", chat.ErrInvalidRequest)
	}
	if outputFormat != nil && request.Options.OutputFormat != nil {
		return nil, fmt.Errorf("%w: request options already define output_format", ErrInvalidOutputFormat)
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	prepared := request.Clone()
	if outputFormat != nil {
		prepared.Options.OutputFormat = outputFormat.Clone()
	}
	return prepared, nil
}

func (c Client) valid() bool { return !lo.IsNil(c.model) }
