package openai

import (
	jsonv2 "encoding/json/v2"
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

type TextReasoningField string

const (
	TextReasoningContent TextReasoningField = reasoningContentField
	TextReasoning        TextReasoningField = reasoningField
)

func (t TextReasoningField) valid() bool {
	return t == TextReasoningContent || t == TextReasoning
}

type TextReasoningReplay uint8

const (
	TextReasoningReplayNever TextReasoningReplay = iota
	TextReasoningReplayAlways
	TextReasoningReplayWithToolCalls
)

// Fields names the provider's documented alternatives. Field identity survives
// accumulation so historical reasoning is replayed under the key that issued it.
type TextReasoningConfig struct {
	Fields []TextReasoningField
	Replay TextReasoningReplay
}

func (t TextReasoningConfig) Validate() error {
	if len(t.Fields) == 0 {
		return errors.New("openai: text reasoning fields are required")
	}
	for index, field := range t.Fields {
		if !field.valid() || slices.Contains(t.Fields[:index], field) {
			return fmt.Errorf("openai: invalid or repeated text reasoning field %q", field)
		}
	}
	if t.Replay > TextReasoningReplayWithToolCalls {
		return fmt.Errorf("openai: invalid text reasoning replay mode %d", t.Replay)
	}
	return nil
}

type textReasoningCodec struct {
	provider string
	config   TextReasoningConfig
}

func (t textReasoningCodec) PrepareRequest(source *corechat.Request, target *openaisdk.ChatCompletionNewParams) error {
	wireIndex := 0
	for index, message := range source.Messages {
		if message.Role == corechat.RoleAssistant {
			if err := t.prepareMessage(message, wireIndex, target); err != nil {
				return fmt.Errorf("messages[%d]: %w", index, err)
			}
		}
		wireIndex += wireMessageCount(message)
	}
	if wireIndex != len(target.Messages) {
		return fmt.Errorf("wire message count = %d; mapped source count = %d", len(target.Messages), wireIndex)
	}
	return nil
}

func (t textReasoningCodec) prepareMessage(message corechat.Message, wireIndex int, target *openaisdk.ChatCompletionNewParams) error {
	if wireIndex >= len(target.Messages) || target.Messages[wireIndex].OfAssistant == nil {
		return errors.New("assistant wire mapping is missing")
	}
	var field TextReasoningField
	var text strings.Builder
	var hasToolCalls bool
	for index, part := range message.Parts {
		if part.Kind == corechat.PartToolCall {
			hasToolCalls = true
		}
		if part.Kind != corechat.PartReasoning || part.Text == "" {
			continue
		}
		partField, owned, err := textReasoningFieldFor(part, t.provider)
		if err != nil {
			return fmt.Errorf("parts[%d]: %w", index, err)
		}
		if !owned {
			continue
		}
		if !slices.Contains(t.config.Fields, partField) {
			return fmt.Errorf("parts[%d]: invalid reasoning field %q for %s", index, partField, t.provider)
		}
		if field != "" && field != partField {
			return errors.New("assistant reasoning changes wire field")
		}
		field = partField
		text.WriteString(part.Text)
	}
	replay := t.config.Replay == TextReasoningReplayAlways ||
		(t.config.Replay == TextReasoningReplayWithToolCalls && hasToolCalls)
	if replay && text.Len() != 0 {
		target.Messages[wireIndex].OfAssistant.SetExtraFields(map[string]any{string(field): text.String()})
	}
	return nil
}

func textReasoningFieldFor(part corechat.Part, provider string) (TextReasoningField, bool, error) {
	issuer, _, err := part.Metadata.Decode[string](textReasoningProviderKey)
	if err != nil {
		return "", false, fmt.Errorf("metadata[%q]: %w", textReasoningProviderKey, err)
	}
	if issuer != provider {
		return "", false, nil
	}
	field, found, err := part.Metadata.Decode[TextReasoningField](textReasoningFieldKey)
	if err != nil {
		return "", false, fmt.Errorf("metadata[%q]: %w", textReasoningFieldKey, err)
	}
	if !found || !field.valid() {
		return "", false, fmt.Errorf("invalid reasoning field %q for %s", field, issuer)
	}
	return field, true, nil
}

// Each stream owns its selected wire field; a shared model must not carry one
// request's model-specific reasoning shape into a concurrent request.
type textReasoningStream struct {
	provider string
	config   TextReasoningConfig
	field    TextReasoningField
}

func (t *textReasoningStream) ReasoningDeltas(source openaisdk.ChatCompletionChunkChoiceDelta, _ uint64) ([]reasoningDelta, error) {
	var deltas []reasoningDelta
	for _, field := range t.config.Fields {
		text, err := decodeTextReasoning(source.JSON.ExtraFields, string(field))
		if err != nil {
			return nil, err
		}
		if text == "" {
			continue
		}
		if t.field != "" && t.field != field {
			return nil, fmt.Errorf("%w: reasoning field changed from %q to %q", corechat.ErrInvalidResponse, t.field, field)
		}
		t.field = field
		part, err := NewTextReasoningPart(t.provider, field, text)
		if err != nil {
			return nil, err
		}
		deltas = append(deltas, reasoningDelta{part: part})
	}
	return deltas, nil
}

func decodeTextReasoning(fields map[string]respjson.Field, fieldName string) (string, error) {
	field, found := fields[fieldName]
	if !found || field.Raw() == "" || field.Raw() == "null" {
		return "", nil
	}
	var reasoning string
	if err := jsonv2.Unmarshal([]byte(field.Raw()), &reasoning); err != nil {
		return "", fmt.Errorf("decode %s: %w", fieldName, err)
	}
	return reasoning, nil
}

func textReasoningDeltas(fields map[string]respjson.Field, provider, fieldName string) ([]reasoningDelta, error) {
	reasoning, err := decodeTextReasoning(fields, fieldName)
	if err != nil || reasoning == "" {
		return nil, err
	}
	part, err := NewTextReasoningPart(provider, TextReasoningField(fieldName), reasoning)
	if err != nil {
		return nil, err
	}
	return []reasoningDelta{{part: part}}, nil
}

// NewTextReasoningPart retains the issuing field because providers require the
// same property during replay even when another model uses a different one.
func NewTextReasoningPart(provider string, field TextReasoningField, text string) (corechat.Part, error) {
	if err := validateProvider(provider); err != nil {
		return corechat.Part{}, fmt.Errorf("openai: text reasoning provider: %w", err)
	}
	if !field.valid() {
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
