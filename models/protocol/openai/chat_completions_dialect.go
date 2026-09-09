package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	reasoningContentField    = "reasoning_content"
	reasoningField           = "reasoning"
	textReasoningProviderKey = "openai/reasoning_provider"
	textReasoningFieldKey    = "openai/reasoning_field"
)

// TextReasoningField identifies a provider's plain-text reasoning property.
type TextReasoningField string

// The vocabulary is closed because it names which response property a provider
// puts plain-text reasoning in. OpenAI-compatible vendors disagree on the
// field name, and reading the wrong one silently yields an empty reasoning
// part rather than an error.
const (
	TextReasoningContent TextReasoningField = reasoningContentField
	TextReasoning        TextReasoningField = reasoningField
)

type requestDialect interface {
	PrepareRequest(source *corechat.Request, target *openaisdk.ChatCompletionNewParams) error
}

type responseDialect interface {
	FinalizeMessage(source openaisdk.ChatCompletionMessage, target *corechat.Message) error
	FinalizeDelta(source openaisdk.ChatCompletionChunkChoiceDelta, target *corechat.Message) error
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
	// surface is supported. Unsupported JSON formats use the shared prompt
	// fallback instead of sending an invalid native parameter.
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
	response responseDialect
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

// ReasoningContentDialect maps the common reasoning_content extension while
// treating it as output-only state.
func ReasoningContentDialect(provider string) Dialect {
	codec := textReasoningCodec{provider: provider, field: reasoningContentField}
	return Dialect{Provider: provider, TokenLimitField: TokenLimitMaxTokens, response: codec}
}

// ReasoningContentReplayDialect maps reasoning_content and sends it back on
// every assistant message. Providers select this only when their protocol
// treats historical reasoning as replayable conversation state.
func ReasoningContentReplayDialect(provider string) Dialect {
	codec := textReasoningCodec{provider: provider, field: reasoningContentField, replay: reasoningReplayAlways}
	return Dialect{Provider: provider, TokenLimitField: TokenLimitMaxTokens, request: codec, response: codec}
}

// ReasoningContentToolReplayDialect maps reasoning_content and sends it back
// only on assistant messages containing tool calls.
func ReasoningContentToolReplayDialect(provider string) Dialect {
	codec := textReasoningCodec{provider: provider, field: reasoningContentField, replay: reasoningReplayWithToolCalls}
	return Dialect{Provider: provider, TokenLimitField: TokenLimitMaxTokens, request: codec, response: codec}
}

// ReasoningDialect maps the reasoning extension as output-only state.
func ReasoningDialect(provider string) Dialect {
	codec := textReasoningCodec{provider: provider, field: reasoningField}
	return Dialect{Provider: provider, TokenLimitField: TokenLimitMaxTokens, response: codec}
}

// ReasoningReplayDialect maps the reasoning extension and sends it back on
// every assistant message.
func ReasoningReplayDialect(provider string) Dialect {
	codec := textReasoningCodec{provider: provider, field: reasoningField, replay: reasoningReplayAlways}
	return Dialect{Provider: provider, TokenLimitField: TokenLimitMaxTokens, request: codec, response: codec}
}

type reasoningReplay uint8

const (
	reasoningReplayNever reasoningReplay = iota
	reasoningReplayAlways
	reasoningReplayWithToolCalls
)

type textReasoningCodec struct {
	provider string
	field    string
	replay   reasoningReplay
}

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

func protocolResponseExtensionKey(provider string) string {
	if provider == protocolProvider {
		return ResponseExtensionKey
	}
	return provider + "/openai_response"
}

func protocolStreamChunkExtensionKey(provider string) string {
	if provider == protocolProvider {
		return StreamChunkExtensionKey
	}
	return provider + "/openai_stream_chunk"
}

func (t textReasoningCodec) PrepareRequest(source *corechat.Request, target *openaisdk.ChatCompletionNewParams) error {
	wireIndex := 0
	for i := range source.Messages {
		message := source.Messages[i]
		if message.Role == corechat.RoleAssistant {
			if wireIndex >= len(target.Messages) || target.Messages[wireIndex].OfAssistant == nil {
				return fmt.Errorf("messages[%d]: assistant wire mapping is missing", i)
			}
			reasoning, hasToolCalls, err := assistantReplayState(message, t.provider, t.field)
			if err != nil {
				return fmt.Errorf("messages[%d]: %w", i, err)
			}
			shouldReplay := t.replay == reasoningReplayAlways ||
				(t.replay == reasoningReplayWithToolCalls && hasToolCalls)
			if reasoning != "" && shouldReplay {
				target.Messages[wireIndex].OfAssistant.SetExtraFields(map[string]any{t.field: reasoning})
			}
		}
		wireIndex += wireMessageCount(message)
	}
	if wireIndex != len(target.Messages) {
		return fmt.Errorf("wire message count = %d; mapped source count = %d", len(target.Messages), wireIndex)
	}
	return nil
}

func (t textReasoningCodec) FinalizeMessage(source openaisdk.ChatCompletionMessage, target *corechat.Message) error {
	return prependTextReasoning(source.JSON.ExtraFields, t.provider, t.field, target)
}

func (t textReasoningCodec) FinalizeDelta(source openaisdk.ChatCompletionChunkChoiceDelta, target *corechat.Message) error {
	return prependTextReasoning(source.JSON.ExtraFields, t.provider, t.field, target)
}

func assistantReplayState(message corechat.Message, provider, field string) (reasoning string, hasToolCalls bool, err error) {
	var builder strings.Builder
	for i := range message.Parts {
		switch message.Parts[i].Kind {
		case corechat.PartReasoning:
			issuer, found, decodeErr := message.Parts[i].Metadata.Decode[string](textReasoningProviderKey)
			if decodeErr != nil {
				return "", false, fmt.Errorf("parts[%d].metadata[%q]: %w", i, textReasoningProviderKey, decodeErr)
			}
			wireField, fieldFound, decodeErr := message.Parts[i].Metadata.Decode[string](textReasoningFieldKey)
			if decodeErr != nil {
				return "", false, fmt.Errorf("parts[%d].metadata[%q]: %w", i, textReasoningFieldKey, decodeErr)
			}
			if !found || !fieldFound || issuer != provider || wireField != field {
				continue
			}
			builder.WriteString(message.Parts[i].Text)
		case corechat.PartToolCall:
			hasToolCalls = true
		}
	}
	return builder.String(), hasToolCalls, nil
}

func wireMessageCount(message corechat.Message) int {
	if message.Role == corechat.RoleTool {
		return len(message.Parts)
	}
	return 1
}

func prependTextReasoning(fields map[string]respjson.Field, provider, fieldName string, target *corechat.Message) error {
	field, ok := fields[fieldName]
	if !ok || field.Raw() == "" || field.Raw() == "null" {
		return nil
	}
	var reasoning string
	if err := json.Unmarshal([]byte(field.Raw()), &reasoning); err != nil {
		return fmt.Errorf("decode %s: %w", fieldName, err)
	}
	if reasoning == "" {
		return nil
	}
	part, err := NewTextReasoningPart(provider, TextReasoningField(fieldName), reasoning)
	if err != nil {
		return err
	}
	target.Parts = append([]corechat.Part{part}, target.Parts...)
	return nil
}

// NewTextReasoningPart records which compatible provider field supplied text
// reasoning so a later request can reconstruct the same wire shape.
func NewTextReasoningPart(provider string, field TextReasoningField, text string) (corechat.Part, error) {
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(provider) != provider {
		return corechat.Part{}, errors.New("openai: text reasoning provider is required and must not have surrounding whitespace")
	}
	if field != TextReasoningContent && field != TextReasoning {
		return corechat.Part{}, fmt.Errorf("openai: unsupported text reasoning field %q", field)
	}
	if text == "" {
		return corechat.Part{}, errors.New("openai: text reasoning is required")
	}
	part := corechat.NewReasoningPart(text, nil)
	if err := part.Metadata.Set(textReasoningProviderKey, provider); err != nil {
		return corechat.Part{}, err
	}
	if err := part.Metadata.Set(textReasoningFieldKey, field); err != nil {
		return corechat.Part{}, err
	}
	return part, nil
}
