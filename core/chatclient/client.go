package chatclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

var (
	// ErrNilModel rejects a client whose only required capability is absent.
	ErrNilModel = errors.New("chatclient: nil model")
	// ErrNilClient identifies use of a zero-value Client.
	ErrNilClient = errors.New("chatclient: nil client")
)

// Client is an immutable, concurrency-safe composition of chat capabilities
// and middleware. It does not make an underlying model concurrency
// safe; callers must still follow the model's concurrency contract.
//
// Call accepts ordinary [chat.Request] values and snapshots requests before
// middleware or provider execution. Streaming has its own required capability
// and construction boundary in [StreamClient].
type Client struct {
	model chat.Model
}

// New binds the required call capability and composes middleware in order.
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
		return zero, ErrNilClient
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
		return nil, ErrNilClient
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
	prepared := request.Clone()
	if outputFormat != nil {
		prepared.Options.OutputFormat = outputFormat.Clone()
	}
	if err := prepared.Validate(); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (c Client) valid() bool { return !lo.IsNil(c.model) }
