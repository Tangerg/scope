package protocol

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	Provider       string
	Client         ClientConfig
	DefaultOptions corechat.Options
}

func (c ChatConfig) Validate() error {
	if err := validateProvider(c.Provider); err != nil {
		return fmt.Errorf("google: Provider: %w", err)
	}
	if err := c.Client.Validate(); err != nil {
		return err
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("google: DefaultOptions: %w", err)
	}
	return nil
}

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
)

// Chat implements the minimal Core Model and optional Streamer capabilities.
type Chat struct {
	api      *api
	defaults corechat.Options
	provider string
}

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(ctx context.Context, config ChatConfig) (*Chat, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}
	return &Chat{api: api, defaults: config.DefaultOptions.Clone(), provider: config.Provider}, nil
}

func (c *Chat) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	modelName, contents, config, err := c.buildProtocolRequest(req)
	if err != nil {
		return nil, err
	}
	response, err := c.api.chatCompletion(ctx, modelName, contents, config)
	if err != nil {
		return nil, err
	}
	mapper := newProtocolResponseMapper(c.provider)
	return mapper.mapResponse(modelName, response)
}

// Stream performs one streaming GenerateContent request. Candidate and logical
// part offsets are retained only for the lifetime of this stream.
func (c *Chat) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		modelName, contents, config, err := c.buildProtocolRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		mapper := newProtocolResponseMapper(c.provider)
		var terminal *corechat.ResponseDelta
		for response, streamErr := range c.api.chatCompletionStream(ctx, modelName, contents, config) {
			if streamErr != nil {
				yield(nil, streamErr)
				return
			}
			mapped, mapErr := mapper.mapDelta(modelName, response)
			if mapErr != nil {
				yield(nil, mapErr)
				return
			}
			if terminal != nil {
				if !yield(terminal, nil) {
					return
				}
				terminal = mapped
				continue
			}
			if mapper.finished() {
				terminal = mapped
				continue
			}
			if !yield(mapped, nil) {
				return
			}
		}
		terminal, err = mapper.complete(terminal)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(terminal, nil)
	}
}

func (c *Chat) buildProtocolRequest(req *corechat.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	if c == nil || c.api == nil {
		return "", nil, nil, errors.New("google: nil Chat")
	}
	if err := req.Validate(); err != nil {
		return "", nil, nil, fmt.Errorf("google: request: %w", err)
	}
	return mapProtocolRequest(c.provider, c.defaults, req)
}
