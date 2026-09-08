package ollama

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
)

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatConfig) Validate() error {
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("ollama: DefaultOptions: %w", err)
	}
	return nil
}

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
)

// Chat implements Core chat over Ollama's native /api/chat endpoint.
type Chat struct {
	api      *api
	defaults corechat.Options
}

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(_ context.Context, config ChatConfig) (*Chat, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(apiConfig{BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	return &Chat{api: api, defaults: config.DefaultOptions.Clone()}, nil
}

func (c *Chat) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	apiReq, err := c.buildProtocolRequest(req, false)
	if err != nil {
		return nil, err
	}

	var (
		response nativeChatResponse
		received bool
	)
	if err := c.api.chat(ctx, apiReq, func(next nativeChatResponse) error {
		response = next
		received = true
		return nil
	}); err != nil {
		return nil, err
	}
	if !received {
		return nil, errors.New("ollama: chat returned no response")
	}
	return newProtocolResponseMapper().mapResponse(apiReq.Model, response)
}

// Stream bridges Ollama's callback stream into Core's pull sequence. Returning
// false from yield aborts the HTTP stream without surfacing a cancellation
// error to the caller.
func (c *Chat) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		apiReq, err := c.buildProtocolRequest(req, true)
		if err != nil {
			yield(nil, err)
			return
		}

		mapper := newProtocolResponseMapper()
		consumerStopped := false
		err = c.api.chat(ctx, apiReq, func(chunk nativeChatResponse) error {
			mapped, mapErr := mapper.mapDelta(apiReq.Model, chunk)
			if mapErr != nil {
				return mapErr
			}
			if !yield(mapped, nil) {
				consumerStopped = true
				return context.Canceled
			}
			return nil
		})
		if err != nil {
			if !consumerStopped {
				yield(nil, err)
			}
			return
		}
		if !mapper.terminated() {
			yield(nil, fmt.Errorf("ollama: stream: %w: missing terminal response", corechat.ErrInvalidResponse))
		}
	}
}

func (c *Chat) buildProtocolRequest(req *corechat.Request, stream bool) (*nativeChatRequest, error) {
	if c == nil || c.api == nil {
		return nil, errors.New("ollama: nil Chat")
	}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("ollama: request: %w", err)
	}
	return mapProtocolRequest(c.defaults, req, stream)
}
