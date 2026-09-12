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
	"slices"

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

// ReasoningEffort controls Mistral's native reasoning mode. The values are the
// six its chat endpoint documents as the reasoning_effort enum; empty asks the
// model for its own default.
type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
)

func (r ReasoningEffort) Validate() error {
	switch r {
	case "", ReasoningEffortNone, ReasoningEffortMinimal, ReasoningEffortLow,
		ReasoningEffortMedium, ReasoningEffortHigh, ReasoningEffortXHigh:
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
func NewChat(_ context.Context, config ChatConfig) (*Chat, error) {
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

func (c *Chat) buildRequest(request *corechat.Request, stream bool) (*chatCompletionRequest, error) {
	if c == nil || c.api == nil {
		return nil, errors.New("mistral: nil Chat")
	}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("mistral: request: %w", err)
	}
	extension, _, err := request.Options.Extensions.Decode[ChatRequestOptions](RequestExtensionKey)
	if err != nil {
		return nil, fmt.Errorf("mistral: extension %q: %w", RequestExtensionKey, err)
	}
	if validateErr := extension.Validate(); validateErr != nil {
		return nil, fmt.Errorf("mistral: extension %q: %w", RequestExtensionKey, validateErr)
	}
	options, err := c.defaults.Resolve(request.Options)
	if err != nil {
		return nil, fmt.Errorf("mistral: options: %w", err)
	}
	if options.Model == "" {
		return nil, errors.New("mistral: model is required in defaults or request options")
	}
	if options.TopK != nil {
		return nil, errors.New("mistral: options.top_k is not supported")
	}
	if options.Temperature != nil && (*options.Temperature < 0 || *options.Temperature > maximumTemperature) {
		return nil, fmt.Errorf("mistral: options.temperature must be between 0 and %g, got %v", maximumTemperature, *options.Temperature)
	}
	messages, err := mapChatRequestMessages(request.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := mapChatTools(request.Tools)
	if err != nil {
		return nil, err
	}
	responseFormat, err := newResponseFormat(options.OutputFormat)
	if err != nil {
		return nil, err
	}
	toolChoice, parallelToolCalls, err := mapMistralToolChoice(request.ToolChoice)
	if err != nil {
		return nil, err
	}
	// Mistral's reasoning_effort enum is Core's vocabulary without max, so the
	// portable option reaches the wire instead of being dropped -- Core is
	// explicit that an adapter "must not accept the effort and send a request
	// that never carried it". An empty effort leaves whatever the native
	// extension set, because empty means "the model's default" and a caller who
	// set reasoning_effort natively has already chosen.
	if options.ReasoningEffort != "" {
		effort := ReasoningEffort(options.ReasoningEffort)
		if validateErr := effort.Validate(); validateErr != nil {
			return nil, fmt.Errorf("mistral: options.reasoning_effort: %w", validateErr)
		}
		extension.ReasoningEffort = effort
	}
	return &chatCompletionRequest{
		Model:              options.Model,
		Messages:           messages,
		Temperature:        options.Temperature,
		TopP:               options.TopP,
		MaxTokens:          options.MaxOutputTokens,
		Stream:             stream,
		Stop:               slices.Clone(options.Stop),
		PresencePenalty:    options.PresencePenalty,
		FrequencyPenalty:   options.FrequencyPenalty,
		Tools:              tools,
		ToolChoice:         toolChoice,
		ParallelToolCalls:  parallelToolCalls,
		ResponseFormat:     responseFormat,
		ChatRequestOptions: extension,
	}, nil
}
