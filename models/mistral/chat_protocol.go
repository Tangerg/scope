package mistral

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/Tangerg/sse"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	RequestExtensionKey     = "mistral/request"
	responseExtensionKey    = "mistral/response"
	streamChunkExtensionKey = "mistral/chunk"
	nativeFinishReasonKey   = "mistral/native_finish_reason"
	mistralStreamDone       = "[DONE]"
	mistralStreamMaxBytes   = 16 << 20
	responseFormatField     = "response_format"
	maximumTemperature      = 1.5
)

// ReasoningEffort controls Mistral's native reasoning mode.
type ReasoningEffort string

// These are the provider values this adapter recognizes.
const (
	ReasoningEffortHigh ReasoningEffort = "high"
	ReasoningEffortNone ReasoningEffort = "none"
)

func (r ReasoningEffort) Validate() error {
	switch r {
	case "", ReasoningEffortHigh, ReasoningEffortNone:
		return nil
	default:
		return fmt.Errorf("unsupported reasoning effort %q", r)
	}
}

// ChatRequestOptions exposes Mistral-specific Chat Completions parameters that
// have no provider-neutral Core equivalent. Store it under RequestExtensionKey.
type ChatRequestOptions struct {
	ReasoningEffort ReasoningEffort   `json:"reasoning_effort,omitempty"`
	RandomSeed      *int64            `json:"random_seed,omitempty"`
	SafePrompt      *bool             `json:"safe_prompt,omitempty"`
	PromptCacheKey  string            `json:"prompt_cache_key,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	Guardrails      []json.RawMessage `json:"guardrails,omitempty"`
}

func (c ChatRequestOptions) Validate() error {
	if err := c.ReasoningEffort.Validate(); err != nil {
		return err
	}
	for index := range c.Guardrails {
		if !json.Valid(c.Guardrails[index]) {
			return fmt.Errorf("guardrails[%d] contains invalid JSON", index)
		}
	}
	return nil
}

func (c *ChatRequestOptions) UnmarshalJSON(data []byte) error {
	if c == nil {
		return errors.New("mistral: nil ChatRequestOptions")
	}
	var reserved struct {
		ResponseFormat    json.RawMessage `json:"response_format"`
		ToolChoice        json.RawMessage `json:"tool_choice"`
		ParallelToolCalls json.RawMessage `json:"parallel_tool_calls"`
	}
	if err := json.Unmarshal(data, &reserved); err != nil {
		return fmt.Errorf("decode Mistral request options: %w", err)
	}
	if len(reserved.ResponseFormat) != 0 {
		return fmt.Errorf("field %q is owned by chat options output format", responseFormatField)
	}
	if len(reserved.ToolChoice) != 0 {
		return errors.New("field \"tool_choice\" is owned by the Core ToolChoice")
	}
	if len(reserved.ParallelToolCalls) != 0 {
		return errors.New("field \"parallel_tool_calls\" is owned by the Core ToolChoice")
	}
	type wireOptions ChatRequestOptions
	var decoded wireOptions
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("decode Mistral request options: %w", err)
	}
	candidate := ChatRequestOptions(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*c = candidate
	return nil
}

// ChatConfig binds provider access and defaults shared by every chat call.
type ChatConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (c ChatConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("mistral: API key is required")
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("mistral: default options: %w", err)
	}
	return nil
}

var (
	_ corechat.Model    = (*Chat)(nil)
	_ corechat.Streamer = (*Chat)(nil)
)

// Chat implements Mistral's native Chat Completions protocol, including
// structured thinking chunks and their multi-turn replay semantics.
type Chat struct {
	api      *api
	defaults corechat.Options
}

// NewChat rejects an invalid provider binding before the first chat call.
func NewChat(config ChatConfig) (*Chat, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    cmp.Or(config.BaseURL, DefaultBaseURL),
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	return &Chat{api: api, defaults: config.DefaultOptions.Clone()}, nil
}

func (c *Chat) Call(ctx context.Context, request *corechat.Request) (*corechat.Response, error) {
	wireRequest, err := c.buildRequest(request, false)
	if err != nil {
		return nil, err
	}
	wireResponse, err := c.api.chatCompletion(ctx, wireRequest)
	if err != nil {
		return nil, err
	}
	return mapChatCompletion(wireResponse)
}

func (c *Chat) Stream(ctx context.Context, request *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
	return func(yield func(*corechat.ResponseDelta, error) bool) {
		wireRequest, err := c.buildRequest(request, true)
		if err != nil {
			yield(nil, err)
			return
		}
		body, err := c.api.chatCompletionStream(ctx, wireRequest)
		if err != nil {
			yield(nil, err)
			return
		}
		defer body.Close()

		events := sse.NewReader(body)
		events.MaxLineBytes = mistralStreamMaxBytes
		events.MaxEventBytes = mistralStreamMaxBytes
		state := newChatStreamState()
		for event, eventErr := range events.Messages() {
			if eventErr != nil {
				yield(nil, fmt.Errorf("mistral: read chat stream: %w", eventErr))
				return
			}
			data := bytes.TrimSpace(event.Data)
			if bytes.Equal(data, []byte(mistralStreamDone)) {
				break
			}
			var chunk chatCompletionChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				yield(nil, fmt.Errorf("mistral: decode chat stream chunk: %w", err))
				return
			}
			response, err := state.mapChunk(chunk)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(response, nil) {
				return
			}
		}
		if !state.terminated() {
			yield(nil, fmt.Errorf("mistral: stream: %w: missing terminal response", corechat.ErrInvalidResponse))
		}
	}
}
