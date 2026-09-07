package chatclient

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

var (
	// ErrNilModel rejects a client whose only required capability is absent.
	ErrNilModel = errors.New("chatclient: nil model")
	// ErrNilClient identifies use of a zero-value Client.
	ErrNilClient = errors.New("chatclient: nil client")
	// ErrStreamingUnsupported reports that no explicit or discovered Streamer is
	// available.
	ErrStreamingUnsupported = errors.New("chatclient: streaming unsupported")
)

var errNilStreamSequence = errors.New("chatclient: streamer returned a nil sequence")

// Client is an immutable, concurrency-safe composition of chat capabilities
// and middleware. It does not make an underlying model concurrency
// safe; callers must still follow the model's concurrency contract.
//
// Call and Stream accept ordinary [chat.Request] values directly. Client
// snapshots each request before invoking middleware or a provider, so those
// layers cannot mutate caller-owned protocol values. A configured Streamer
// takes precedence; otherwise New discovers the capability on the model. Stream
// remains lazy and reports unsupported streaming as its single terminal item.
type Client struct {
	model    chat.Model
	streamer chat.Streamer
}

// New snapshots middleware and discovers streaming only when Config does not
// provide an explicit Streamer. The returned Client has no mutable defaults.
func New(model chat.Model, config Config) (Client, error) {
	if lo.IsNil(model) {
		return Client{}, ErrNilModel
	}

	config, err := config.snapshot()
	if err != nil {
		return Client{}, err
	}

	streamer := config.Streamer
	if streamer == nil {
		streamer, _ = model.(chat.Streamer)
	}
	model = chat.Wrap(model, config.CallMiddleware...)
	if lo.IsNil(model) {
		return Client{}, errors.New("chatclient: call middleware returned a nil model")
	}
	if streamer != nil {
		streamer = chat.WrapStream(streamer, config.StreamMiddleware...)
		if lo.IsNil(streamer) {
			return Client{}, errors.New("chatclient: stream middleware returned a nil streamer")
		}
	}

	return Client{
		model:    model,
		streamer: streamer,
	}, nil
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
	prepared, err := c.prepareRequest(req, format)
	if err != nil {
		return nil, err
	}
	return c.model.Call(ctx, prepared)
}

func (c Client) prepareRequest(request *chat.Request, outputFormat *chat.OutputFormat) (*chat.Request, error) {
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

// Stream returns a lazy sequence; lack of streaming support is yielded as its
// single terminal error rather than hidden behind a capability probe.
func (c Client) Stream(ctx context.Context, req *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
	if !c.valid() {
		return errorSequence(ErrNilClient)
	}
	return c.stream(ctx, req, nil)
}

func (c Client) stream(ctx context.Context, req *chat.Request, format *chat.OutputFormat) iter.Seq2[*chat.ResponseDelta, error] {
	prepared, err := c.prepareRequest(req, format)
	if err != nil {
		return errorSequence(err)
	}
	if c.streamer == nil {
		return errorSequence(ErrStreamingUnsupported)
	}

	return func(yield func(*chat.ResponseDelta, error) bool) {
		sequence := c.streamer.Stream(ctx, prepared)
		if sequence == nil {
			yield(nil, errNilStreamSequence)
			return
		}
		sequence(yield)
	}
}

func (c Client) valid() bool {
	return !lo.IsNil(c.model)
}

func errorSequence(err error) iter.Seq2[*chat.ResponseDelta, error] {
	return func(yield func(*chat.ResponseDelta, error) bool) {
		yield(nil, err)
	}
}
