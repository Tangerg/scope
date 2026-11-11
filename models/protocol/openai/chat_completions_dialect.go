package openai

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	CompatibleRequestExtensionSuffix = "/openai_request"
	CompatibleStreamExtensionSuffix  = "/openai_stream_chunk"
	nativeFinishReasonSuffix         = "/native_finish_reason"
)

const (
	assistantFieldRole         = "role"
	assistantFieldContent      = "content"
	assistantFieldName         = "name"
	assistantFieldToolCalls    = "tool_calls"
	assistantFieldFunctionCall = "function_call"
	assistantFieldRefusal      = "refusal"
	assistantFieldAudio        = "audio"
)

type requestDialect interface {
	PrepareRequest(source *corechat.Request, target *openaisdk.ChatCompletionNewParams) error
}

// responseDialect maps the provider's reasoning fields of one stream chunk.
type responseDialect interface {
	ReasoningDeltas(source openaisdk.ChatCompletionChunkChoiceDelta, sequence uint64) ([]reasoningDelta, error)
}

// reasoningDelta is one reasoning increment and the native identity of the
// block it extends. The zero block is the provider's plain reasoning field.
type reasoningDelta struct {
	block string
	part  corechat.Part
}

// CompatibleRequest exposes the stable subset of a compatible request that a
// provider dialect may inspect. Core-owned fields cannot be replaced; OpenAI
// SDK wire types never cross this boundary.
type CompatibleRequest struct {
	model          string
	temperature    *float64
	topP           *float64
	stream         bool
	extraFields    map[string]any
	finalAssistant *openaisdk.ChatCompletionAssistantMessageParam
}

// Model returns the effective model after Core defaults and request options
// have been merged.
func (c *CompatibleRequest) Model() string { return c.model }

// Temperature returns the effective temperature when one was supplied.
func (c *CompatibleRequest) Temperature() (float64, bool) {
	if c.temperature == nil {
		return 0, false
	}
	return *c.temperature, true
}

// TopP returns the effective nucleus-sampling value when one was supplied. It
// is exposed for the same reason as Temperature: a provider that overrides or
// bounds it can only say so about the value that will actually be sent, which
// is the merged one rather than the request's own.
func (c *CompatibleRequest) TopP() (float64, bool) {
	if c.topP == nil {
		return 0, false
	}
	return *c.topP, true
}

func (c *CompatibleRequest) Stream() bool { return c.stream }

// SetExtraField adds or replaces one provider-owned top-level JSON field.
func (c *CompatibleRequest) SetExtraField(name string, value any) error {
	if err := validateChatExtraField(name); err != nil {
		return err
	}
	if c.extraFields == nil {
		c.extraFields = make(map[string]any)
	}
	c.extraFields[name] = value
	return nil
}

// Provider prefix-continuation flags belong to the final assistant message,
// while its role, content and tool calls remain owned by the Core projection.
func (c *CompatibleRequest) SetFinalAssistantExtraField(name string, value any) error {
	if c.finalAssistant == nil {
		return errors.New("openai: final message must be an assistant message")
	}
	if name == "" || strings.TrimSpace(name) != name {
		return errors.New("openai: assistant extra field name must be nonblank and unpadded")
	}
	switch name {
	case assistantFieldRole, assistantFieldContent, assistantFieldName, assistantFieldToolCalls, assistantFieldFunctionCall, assistantFieldRefusal, assistantFieldAudio:
		return fmt.Errorf("openai: assistant field %q is owned by Core", name)
	}
	fields := c.finalAssistant.ExtraFields()
	if _, exists := fields[name]; exists {
		return fmt.Errorf("openai: assistant field %q already has a projection", name)
	}
	fields = maps.Clone(fields)
	if fields == nil {
		fields = make(map[string]any)
	}
	fields[name] = value
	c.finalAssistant.SetExtraFields(fields)
	return nil
}

// DialectConfig groups the independently typed request and response protocol facets
// selected by a provider adapter. One Chat Completions adapter has exactly one
// dialect. Construction snapshots its mutable policy values; callback captures
// remain owned by the caller. PrepareRequest receives an independently owned
// request with resolved options after Core projection. Only target changes
// affect the wire request; source edits never change the caller's history.
type DialectConfig struct {
	// Provider scopes provider-owned response and replay state.
	Provider        string
	PrepareRequest  func(source *corechat.Request, target *CompatibleRequest) error
	TokenLimitField TokenLimitField
	// SupportsTopK enables the Core option's wire projection for compatible
	// endpoints that support it. Raw request extensions never own this field.
	SupportsTopK bool
	// NativeOutputFormat reports whether this compatible endpoint natively
	// supports a Core output format. A nil function means the full OpenAI response_format
	// surface is supported. Unsupported formats return ErrUnsupportedOutputFormat.
	NativeOutputFormat func(corechat.OutputFormatType) bool
	// DisableRawRequestExtension prevents provider adapters from accepting an
	// arbitrary OpenAI request object when their documented request surface is
	// narrower than OpenAI's.
	DisableRawRequestExtension bool
	// IgnoredOptions names the Core options this endpoint accepts on the wire
	// and then discards. A compatible provider that publishes such a list --
	// Anthropic's marks reasoning_effort, presence_penalty and
	// frequency_penalty "Ignored" -- makes a populated option vanish between
	// Call and the model, which reads exactly like the adapter dropping it.
	// Naming them here refuses the option instead, so the caller learns at the
	// call that this endpoint cannot honor it.
	IgnoredOptions []ChatOption
	// MaxTemperature bounds Options.Temperature for a provider that documents a
	// narrower range than OpenAI's. Refusing beats sending an out-of-range
	// value, whose fate the provider decides: Anthropic's table says it is
	// "capped at 1", which alters the request silently, while others answer an
	// error further from the caller. Nil accepts whatever Core accepts.
	MaxTemperature *float64

	TextReasoning    *TextReasoningConfig
	ReasoningDetails *ReasoningDetailsConfig
}

func (d DialectConfig) Validate() error {
	if err := validateProvider(d.Provider); err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	if !d.TokenLimitField.Valid() {
		return fmt.Errorf("token limit field %q is invalid", d.TokenLimitField)
	}
	if d.TextReasoning != nil && d.ReasoningDetails != nil {
		return errors.New("reasoning wire representation must have exactly one owner")
	}
	if d.TextReasoning != nil {
		return d.TextReasoning.Validate()
	}
	if d.ReasoningDetails != nil {
		if len(d.Provider) > int(^uint16(0)) {
			return errors.New("reasoning provider exceeds framing limit")
		}
		return d.ReasoningDetails.Validate()
	}
	return nil
}

func (d DialectConfig) bind() (chatDialect, error) {
	if err := d.Validate(); err != nil {
		return chatDialect{}, err
	}
	d.IgnoredOptions = slices.Clone(d.IgnoredOptions)
	if d.MaxTemperature != nil {
		d.MaxTemperature = new(*d.MaxTemperature)
	}
	bound := chatDialect{DialectConfig: d}
	if d.TextReasoning != nil {
		config := *d.TextReasoning
		config.Fields = slices.Clone(config.Fields)
		bound.TextReasoning = &config
		bound.request = textReasoningCodec{provider: d.Provider, config: config}
		bound.response = func() responseDialect { return &textReasoningStream{provider: d.Provider, config: config} }
	}
	if d.ReasoningDetails != nil {
		config := *d.ReasoningDetails
		bound.ReasoningDetails = &config
		codec := reasoningDetailsCodec{provider: d.Provider, config: config}
		bound.request = codec
		bound.response = func() responseDialect { return codec }
	}
	return bound, nil
}

// Only construction can bind the namespace. Codec issuer and extension keys
// are immutable projections of this binding, never independently editable facts.
type chatDialect struct {
	DialectConfig
	request  requestDialect
	response func() responseDialect
}

func (c chatDialect) ignores(option ChatOption) bool {
	return slices.Contains(c.IgnoredOptions, option)
}

// ChatOption names one Core chat option by the OpenAI request field it travels
// as. It exists so a dialect can declare which of them its provider discards.
type ChatOption string

const (
	ChatOptionFrequencyPenalty ChatOption = "frequency_penalty"
	ChatOptionPresencePenalty  ChatOption = "presence_penalty"
	ChatOptionReasoningEffort  ChatOption = "reasoning_effort"
	ChatOptionTemperature      ChatOption = "temperature"
	ChatOptionTopP             ChatOption = "top_p"
)

// TokenLimitField identifies the provider's wire field for Core's neutral
// Options.MaxOutputTokens value. OpenAI-compatible APIs are not uniform here:
// legacy-compatible providers accept max_tokens while newer protocols use
// max_completion_tokens.
type TokenLimitField string

const (
	// TokenLimitMaxTokens selects the legacy-compatible max_tokens field.
	TokenLimitMaxTokens TokenLimitField = "max_tokens"
	// TokenLimitMaxCompletionTokens selects max_completion_tokens.
	TokenLimitMaxCompletionTokens TokenLimitField = "max_completion_tokens"
)

func (t TokenLimitField) Valid() bool {
	return t == TokenLimitMaxTokens || t == TokenLimitMaxCompletionTokens
}

func (t TokenLimitField) String() string { return string(t) }

func protocolRequestExtensionKey(provider string) string {
	if provider == ProviderID {
		return RequestExtensionKey
	}
	return provider + CompatibleRequestExtensionSuffix
}

func validateProvider(provider string) error {
	if provider == "" || strings.TrimSpace(provider) != provider || strings.Contains(provider, "/") {
		return errors.New("provider is required, must not contain '/', and must not have surrounding whitespace")
	}
	return nil
}

func protocolStreamChunkExtensionKey(provider string) string {
	if provider == ProviderID {
		return StreamChunkExtensionKey
	}
	return provider + CompatibleStreamExtensionSuffix
}

func wireMessageCount(message corechat.Message) int {
	if message.Role == corechat.RoleTool {
		return len(message.Parts)
	}
	return 1
}
