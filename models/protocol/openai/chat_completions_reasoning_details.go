package openai

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"

	corechat "github.com/Tangerg/scope/core/chat"
)

const reasoningDetailFrameHeaderSize = 10

var reasoningDetailFrameMagic = [4]byte{'L', 'Y', 'R', 'D'}

// ReasoningDetailsConfig describes the structured reasoning_details dialect
// shared by several OpenAI-compatible providers. Provider scopes opaque replay
// state so one provider's signed details are never sent to another provider.
type ReasoningDetailsConfig struct {
	Provider        string
	TextField       string
	DetailsField    string
	ReplayPlainText bool
}

func (r ReasoningDetailsConfig) Validate() error {
	if strings.TrimSpace(r.Provider) == "" {
		return errors.New("openai: reasoning details provider is required")
	}
	if strings.TrimSpace(r.Provider) != r.Provider {
		return errors.New("openai: reasoning details provider must not have surrounding whitespace")
	}
	if len(r.Provider) > int(^uint16(0)) {
		return errors.New("openai: reasoning details provider exceeds framing limit")
	}
	if r.TextField == "" {
		return errors.New("openai: reasoning details text field is required")
	}
	if r.DetailsField == "" {
		return errors.New("openai: reasoning details field is required")
	}
	return nil
}

// ReasoningDetailsDialect preserves structured reasoning details losslessly in
// Core reasoning signatures. The resulting signatures are safe to concatenate
// while accumulating streaming deltas and are replayed only to the provider
// that produced them.
func ReasoningDetailsDialect(config ReasoningDetailsConfig) (Dialect, error) {
	if err := config.Validate(); err != nil {
		return Dialect{}, err
	}
	codec := reasoningDetailsCodec{config: config}
	return Dialect{
		Provider: config.Provider, TokenLimitField: TokenLimitMaxTokens,
		request: codec, response: codec,
	}, nil
}

type reasoningDetailsCodec struct {
	config ReasoningDetailsConfig
}

func (r reasoningDetailsCodec) PrepareRequest(source *corechat.Request, target *openaisdk.ChatCompletionNewParams) error {
	wireIndex := 0
	for messageIndex := range source.Messages {
		message := source.Messages[messageIndex]
		if err := r.prepareMessage(messageIndex, wireIndex, message, target); err != nil {
			return err
		}
		wireIndex += wireMessageCount(message)
	}
	if wireIndex != len(target.Messages) {
		return fmt.Errorf("wire message count = %d; mapped source count = %d", len(target.Messages), wireIndex)
	}
	return nil
}

func (r reasoningDetailsCodec) prepareMessage(messageIndex, wireIndex int, message corechat.Message, target *openaisdk.ChatCompletionNewParams) error {
	if message.Role != corechat.RoleAssistant {
		return nil
	}
	if wireIndex >= len(target.Messages) || target.Messages[wireIndex].OfAssistant == nil {
		return fmt.Errorf("messages[%d]: assistant wire mapping is missing", messageIndex)
	}
	details, plainReasoning, err := r.mapHistory(message.Parts)
	if err != nil {
		return fmt.Errorf("messages[%d]: %w", messageIndex, err)
	}
	extraFields := make(map[string]any)
	if len(details) > 0 {
		extraFields[r.config.DetailsField] = details
	}
	if plainReasoning != "" && r.config.ReplayPlainText {
		extraFields[r.config.TextField] = plainReasoning
	}
	if len(extraFields) > 0 {
		target.Messages[wireIndex].OfAssistant.SetExtraFields(extraFields)
	}
	return nil
}

func (r reasoningDetailsCodec) FinalizeDelta(source openaisdk.ChatCompletionChunkChoiceDelta, target *corechat.Message) error {
	return r.prependReasoning(source.JSON.ExtraFields, target)
}

func (r reasoningDetailsCodec) prependReasoning(fields map[string]respjson.Field, target *corechat.Message) error {
	detailsField, hasDetails := fields[r.config.DetailsField]
	if hasDetails && detailsField.Raw() != "" && detailsField.Raw() != "null" {
		parts, err := r.decodeDetails([]byte(detailsField.Raw()))
		if err != nil {
			return err
		}
		if len(parts) > 0 {
			target.Parts = append(parts, target.Parts...)
			return nil
		}
	}
	return prependTextReasoning(fields, r.config.Provider, r.config.TextField, target)
}

func (r reasoningDetailsCodec) decodeDetails(raw []byte) ([]corechat.Part, error) {
	var details []json.RawMessage
	if err := json.Unmarshal(raw, &details); err != nil {
		return nil, fmt.Errorf("%s: decode %s: %w", r.config.Provider, r.config.DetailsField, err)
	}
	parts := make([]corechat.Part, 0, len(details))
	for index := range details {
		part, err := r.decodeDetail(details[index])
		if err != nil {
			return nil, fmt.Errorf("%s: %s[%d]: %w", r.config.Provider, r.config.DetailsField, index, err)
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func (r reasoningDetailsCodec) decodeDetail(raw json.RawMessage) (corechat.Part, error) {
	var detail struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		return corechat.Part{}, err
	}
	if detail.Type == "" {
		return corechat.Part{}, errors.New("detail type is required")
	}
	text := ""
	switch detail.Type {
	case "reasoning.text":
		text = detail.Text
	case "reasoning.summary":
		text = detail.Summary
	}
	frame, err := r.encodeFrame(raw)
	if err != nil {
		return corechat.Part{}, err
	}
	return corechat.NewReasoningPart(text, frame), nil
}

func (r reasoningDetailsCodec) encodeFrame(raw json.RawMessage) ([]byte, error) {
	if uint64(len(raw)) > uint64(^uint32(0)) {
		return nil, errors.New("reasoning detail exceeds framing limit")
	}
	providerLength := len(r.config.Provider)
	frame := make([]byte, reasoningDetailFrameHeaderSize+providerLength+len(raw))
	copy(frame, reasoningDetailFrameMagic[:])
	binary.BigEndian.PutUint16(frame[4:6], uint16(providerLength))
	binary.BigEndian.PutUint32(frame[6:reasoningDetailFrameHeaderSize], uint32(len(raw)))
	copy(frame[reasoningDetailFrameHeaderSize:], r.config.Provider)
	copy(frame[reasoningDetailFrameHeaderSize+providerLength:], raw)
	return frame, nil
}

func (r reasoningDetailsCodec) mapHistory(parts []corechat.Part) ([]json.RawMessage, string, error) {
	var frames []json.RawMessage
	var plain strings.Builder
	for partIndex := range parts {
		part := parts[partIndex]
		if part.Kind != corechat.PartReasoning {
			continue
		}
		if len(part.ReasoningState) == 0 {
			plain.WriteString(part.Text)
			continue
		}
		decoded, framed, ownProvider, err := r.decodeFrames(part.ReasoningState)
		if err != nil {
			return nil, "", fmt.Errorf("parts[%d].reasoning signature: %w", partIndex, err)
		}
		if !framed || !ownProvider {
			plain.WriteString(part.Text)
			continue
		}
		frames = append(frames, decoded...)
	}
	details, err := coalesceReasoningDetailFrames(frames)
	if err != nil {
		return nil, "", err
	}
	return details, plain.String(), nil
}

func (r reasoningDetailsCodec) decodeFrames(signature []byte) ([]json.RawMessage, bool, bool, error) {
	if len(signature) < len(reasoningDetailFrameMagic) || !bytes.Equal(signature[:len(reasoningDetailFrameMagic)], reasoningDetailFrameMagic[:]) {
		return nil, false, false, nil
	}
	var frames []json.RawMessage
	ownProvider := true
	for offset := 0; offset < len(signature); {
		if len(signature)-offset < reasoningDetailFrameHeaderSize {
			return nil, true, false, errors.New("truncated frame header")
		}
		if !bytes.Equal(signature[offset:offset+4], reasoningDetailFrameMagic[:]) {
			return nil, true, false, fmt.Errorf("invalid frame magic at byte %d", offset)
		}
		providerLength := int(binary.BigEndian.Uint16(signature[offset+4 : offset+6]))
		payloadLength := int(binary.BigEndian.Uint32(signature[offset+6 : offset+reasoningDetailFrameHeaderSize]))
		offset += reasoningDetailFrameHeaderSize
		if providerLength > len(signature)-offset {
			return nil, true, false, fmt.Errorf("provider length %d exceeds remaining %d bytes", providerLength, len(signature)-offset)
		}
		provider := string(signature[offset : offset+providerLength])
		offset += providerLength
		if payloadLength > len(signature)-offset {
			return nil, true, false, fmt.Errorf("frame length %d exceeds remaining %d bytes", payloadLength, len(signature)-offset)
		}
		raw := json.RawMessage(bytes.Clone(signature[offset : offset+payloadLength]))
		if !json.Valid(raw) {
			return nil, true, false, errors.New("frame contains invalid JSON")
		}
		if provider != r.config.Provider {
			ownProvider = false
		} else {
			frames = append(frames, raw)
		}
		offset += payloadLength
	}
	return frames, true, ownProvider, nil
}

func coalesceReasoningDetailFrames(frames []json.RawMessage) ([]json.RawMessage, error) {
	if len(frames) <= 1 {
		return frames, nil
	}
	result := make([]json.RawMessage, 0, len(frames))
	for frameIndex := range frames {
		if len(result) == 0 {
			result = append(result, frames[frameIndex])
			continue
		}
		merged, same, err := mergeReasoningDetail(result[len(result)-1], frames[frameIndex])
		if err != nil {
			return nil, fmt.Errorf("reasoning detail frame %d: %w", frameIndex, err)
		}
		if same {
			result[len(result)-1] = merged
		} else {
			result = append(result, frames[frameIndex])
		}
	}
	return result, nil
}

func mergeReasoningDetail(leftRaw, rightRaw json.RawMessage) (json.RawMessage, bool, error) {
	var left, right map[string]json.RawMessage
	if err := json.Unmarshal(leftRaw, &left); err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(rightRaw, &right); err != nil {
		return nil, false, err
	}
	if !sameReasoningDetailIdentity(left, right) {
		return nil, false, nil
	}
	for _, field := range []string{"text", "summary", "data", "signature"} {
		leftValue, _, err := decodeOptionalString(left[field])
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", field, err)
		}
		rightValue, rightOK, err := decodeOptionalString(right[field])
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", field, err)
		}
		if !rightOK {
			continue
		}
		encoded, err := json.Marshal(leftValue + rightValue)
		if err != nil {
			return nil, false, err
		}
		left[field] = encoded
	}
	merged, err := json.Marshal(left)
	return merged, true, err
}

func sameReasoningDetailIdentity(left, right map[string]json.RawMessage) bool {
	for _, field := range []string{"type", "id", "format", "index"} {
		if !bytes.Equal(left[field], right[field]) {
			return false
		}
	}
	return len(left["id"]) > 0 || len(left["index"]) > 0
}

func decodeOptionalString(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, err
	}
	return value, true, nil
}
