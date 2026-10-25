package openai

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
)

type requestDialect interface {
	PrepareRequest(source *corechat.Request, target *openaisdk.ChatCompletionNewParams) error
}

type responseDialect interface {
	FinalizeDelta(source openaisdk.ChatCompletionChunkChoiceDelta, target *corechat.Message, sequence uint64) error
}

// CompatibleRequest exposes the stable subset of a compatible request that a
// provider dialect may inspect. Provider-only top-level JSON fields are added
// with SetExtraField; OpenAI SDK wire types never cross this boundary.
type CompatibleRequest struct {
	model       string
	temperature *float64
	topP        *float64
	stream      bool
	extraFields map[string]any
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

// Stream reports whether the request will use the streaming endpoint.
func (c *CompatibleRequest) Stream() bool { return c.stream }

// SetExtraField adds or replaces one provider-owned top-level JSON field.
func (c *CompatibleRequest) SetExtraField(name string, value any) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return errors.New("openai: extra field name is required and must not have surrounding whitespace")
	}
	if coreOwnedChatField(name) {
		return fmt.Errorf("openai: extra field %q is owned by Core", name)
	}
	if c.extraFields == nil {
		c.extraFields = make(map[string]any)
	}
	c.extraFields[name] = value
	return nil
}

// Dialect groups the independently typed request and response protocol facets
// selected by a provider adapter. One Chat Completions adapter has exactly one
// dialect.
type Dialect struct {
	// Provider scopes provider-owned response and replay state.
	Provider        string
	PrepareRequest  func(source *corechat.Request, target *CompatibleRequest) error
	TokenLimitField TokenLimitField
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

	request  requestDialect
	response func() responseDialect
}

func (d Dialect) Validate() error {
	if err := validateProvider(d.Provider); err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	if !d.TokenLimitField.Valid() {
		return fmt.Errorf("token limit field %q is invalid", d.TokenLimitField)
	}
	return nil
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

func (d Dialect) ignores(option ChatOption) bool {
	return slices.Contains(d.IgnoredOptions, option)
}

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
	if provider == protocolProvider {
		return RequestExtensionKey
	}
	return provider + "/openai_request"
}

func validateProvider(provider string) error {
	if provider == "" || strings.TrimSpace(provider) != provider || strings.Contains(provider, "/") {
		return errors.New("provider is required, must not contain '/', and must not have surrounding whitespace")
	}
	return nil
}

func protocolModalityRequestExtensionKey(provider, modality string) string {
	return provider + "/" + modality + "_request"
}

func protocolStreamChunkExtensionKey(provider string) string {
	if provider == protocolProvider {
		return StreamChunkExtensionKey
	}
	return provider + "/openai_stream_chunk"
}

func wireMessageCount(message corechat.Message) int {
	if message.Role == corechat.RoleTool {
		return len(message.Parts)
	}
	return 1
}
