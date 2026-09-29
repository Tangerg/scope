package chatclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

var (
	ErrNilModel      = errors.New("chatclient: nil model")
	ErrInvalidClient = errors.New("chatclient: uninitialized client")
)

// Client is an immutable composition of a chat model and its middleware. It is
// as safe for concurrent use as the underlying model. Streaming has its own
// construction boundary in [StreamClient].
type Client struct {
	model chat.Model
}

func New(model chat.Model, config Config) (Client, error) {
	if lo.IsNil(model) {
		return Client{}, ErrNilModel
	}
	model = chat.Wrap(model, config.CallMiddleware...)
	if lo.IsNil(model) {
		return Client{}, errors.New("chatclient: call middleware returned a nil model")
	}
	return Client{model: model}, nil
}

// Output asks the provider to enforce format, then strictly decodes naturally
// completed text. Refusal and other non-successful completion reasons return
// OutputCompletionError. Media and tool requests cannot become typed values.
// Output never repairs JSON or injects format instructions into the prompt.
func (c Client) Output[T any](ctx context.Context, req *chat.Request, format OutputFormat[T]) (T, error) {
	var zero T
	if !c.valid() {
		return zero, ErrInvalidClient
	}
	if err := format.validate(); err != nil {
		return zero, err
	}
	response, err := c.call(ctx, req, &format.contract)
	return format.decodeResponse(response, err)
}

// Call snapshots and validates req before the middleware and model boundary.
func (c Client) Call(ctx context.Context, req *chat.Request) (*chat.Response, error) {
	if !c.valid() {
		return nil, ErrInvalidClient
	}
	return c.call(ctx, req, nil)
}

func (c Client) call(ctx context.Context, req *chat.Request, format *chat.OutputFormat) (*chat.Response, error) {
	prepared, err := prepareRequest(req, format)
	if err != nil {
		return nil, err
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
