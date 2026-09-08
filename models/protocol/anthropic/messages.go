package anthropic

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	protocolProvider           = "anthropic"
	protocolDefaultMaxTokens   = 4096
	protocolMaximumTemperature = 1
)

// MessagesConfig configures an Anthropic Messages adapter. Defaults are copied
// during construction; callers may select the model per request.
type MessagesConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
	Headers        http.Header
}

func (m MessagesConfig) Validate() error {
	if m.APIKey == "" {
		return errors.New("anthropic: API key is required")
	}
	if err := m.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("anthropic: default options: %w", err)
	}
	return nil
}

var (
	_ corechat.Model    = (*Messages)(nil)
	_ corechat.Streamer = (*Messages)(nil)
)

// Messages implements Anthropic's Messages protocol and is also the reusable
// protocol base for provider packages exposing a compatible endpoint.
type Messages struct {
	api      *api
	defaults corechat.Options
	dialect  Dialect
}

// NewMessages rejects an invalid provider binding before the first Messages call.
func NewMessages(_ context.Context, config MessagesConfig) (*Messages, error) {
	return newMessages(config, Dialect{
		Provider: protocolProvider, MaxTemperature: protocolMaximumTemperature,
		RejectTopK: true, RejectTopP: true, NativeJSONSchema: true,
	})
}

// NewCompatibleMessages rejects an invalid compatible binding before the first call.
func NewCompatibleMessages(_ context.Context, config MessagesConfig, dialect Dialect) (*Messages, error) {
	return newMessages(config, dialect)
}

func newMessages(config MessagesConfig, dialect Dialect) (*Messages, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if dialect.Provider == "" || strings.TrimSpace(dialect.Provider) != dialect.Provider || strings.Contains(dialect.Provider, "/") {
		return nil, errors.New("anthropic: dialect provider is required, must not contain '/', and must not have surrounding whitespace")
	}
	api, err := newAPI(apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
		Headers:    config.Headers,
	})
	if err != nil {
		return nil, err
	}
	return &Messages{
		api:      api,
		defaults: config.DefaultOptions.Clone(),
		dialect:  dialect,
	}, nil
}

func (m *Messages) Call(ctx context.Context, req *corechat.Request) (*corechat.Response, error) {
	params, err := m.buildProtocolRequest(req)
	if err != nil {
		return nil, err
	}
	response, err := m.api.chatCompletion(ctx, params)
	if err != nil {
		return nil, err
	}
	mapped, err := mapProtocolMessage(response, m.dialect.Provider)
	if err != nil {
		return nil, err
	}
	return mapped, nil
}

// CountInputTokens calls the provider's Messages token-count endpoint with the
// same provider request projection used by Call.
func (m *Messages) CountInputTokens(ctx context.Context, req *corechat.Request) (int64, error) {
	params, err := m.buildProtocolRequest(req)
	if err != nil {
		return 0, err
	}
	countParams, err := projectMessageInputTokenCount(params)
	if err != nil {
		return 0, err
	}
	response, err := m.api.countTokens(ctx, countParams)
	if err != nil {
		return 0, err
	}
	return response.InputTokens, nil
}

// Stream performs one streaming Messages API request and yields provider
// deltas without accumulating text or tool arguments across events.
func (m *Messages) Stream(ctx context.Context, req *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		params, err := m.buildProtocolRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		stream := m.api.chatCompletionStream(ctx, params)
		if stream == nil {
			yield(nil, errors.New("anthropic: nil stream"))
			return
		}
		defer stream.Close()

		state := newProtocolStreamState(m.dialect.Provider)
		var terminal *corechat.ResponseDelta
		for stream.Next() {
			event := stream.Current()
			response, mapErr := state.mapEvent(event)
			if mapErr != nil {
				yield(nil, mapErr)
				return
			}
			if terminal != nil {
				if !yield(terminal, nil) {
					return
				}
				terminal = response
				continue
			}
			if state.finished() {
				terminal = response
				continue
			}
			if !yield(response, nil) {
				return
			}
		}
		if streamErr := stream.Err(); streamErr != nil {
			yield(nil, m.api.wrapError(streamErr))
			return
		}
		terminal, err = state.complete(terminal)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(terminal, nil)
	}
}

func (m *Messages) buildProtocolRequest(req *corechat.Request) (*anthropicsdk.MessageNewParams, error) {
	if m == nil || m.api == nil {
		return nil, errors.New("anthropic: nil Messages")
	}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("anthropic: request: %w", err)
	}
	params, err := mapProtocolRequest(m.defaults, req, m.dialect)
	if err != nil {
		return nil, err
	}
	return params, nil
}
