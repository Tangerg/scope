package managed

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/agent/run"
)

const (
	mediaTypePlainText = "text/plain"
	mediaTypeJSON      = "application/json"
)

type contentPolicy struct {
	inputs, outputs []string
}

func newContentPolicy(card sdk.AgentCard) (contentPolicy, error) {
	policy := contentPolicy{inputs: slices.Clone(card.DefaultInputModes), outputs: slices.Clone(card.DefaultOutputModes)}
	if len(policy.inputs) == 0 {
		policy.inputs = []string{mediaTypePlainText, mediaTypeJSON}
	}
	if len(policy.outputs) == 0 {
		policy.outputs = []string{mediaTypeJSON}
	}
	for _, modes := range [][]string{policy.inputs, policy.outputs} {
		for _, mode := range modes {
			if mode != mediaTypePlainText && mode != mediaTypeJSON {
				return contentPolicy{}, sdk.ErrUnsupportedContentType
			}
		}
	}
	for _, skill := range card.Skills {
		if len(skill.InputModes) > 0 && !sameModes(skill.InputModes, policy.inputs) || len(skill.OutputModes) > 0 && !sameModes(skill.OutputModes, policy.outputs) {
			return contentPolicy{}, sdk.ErrUnsupportedContentType
		}
	}
	return policy, nil
}

func (c contentPolicy) prepare(request *sdk.SendMessageRequest) (run.Payload, error) {
	if request.Config != nil {
		if err := validateHistoryLength(request.Config.HistoryLength); err != nil {
			return run.Payload{}, err
		}
	}
	content, err := parseMessageContent(request.Message)
	if err != nil {
		return run.Payload{}, err
	}
	if request.Config != nil && request.Config.PushConfig != nil {
		return run.Payload{}, sdk.ErrPushNotificationNotSupported
	}
	if !slices.Contains(c.inputs, content.mediaType) {
		return run.Payload{}, sdk.ErrUnsupportedContentType
	}
	if request.Config != nil && len(request.Config.AcceptedOutputModes) > 0 {
		for _, mode := range c.outputs {
			if !slices.Contains(request.Config.AcceptedOutputModes, mode) {
				return run.Payload{}, sdk.ErrUnsupportedContentType
			}
		}
	}
	return content.payload, nil
}

type messageContent struct {
	payload   run.Payload
	mediaType string
}

// Raw JSON bytes and JSON text preserve numeric lexemes. The SDK's generic
// Data decoder rounds numbers through float64; refuse numeric Data rather
// than allowing a changed value into a run or an idempotency digest.
func parseMessageContent(message *sdk.Message) (messageContent, error) {
	if message == nil || message.Role != sdk.MessageRoleUser || len(message.Parts) != 1 || message.Parts[0] == nil {
		return messageContent{}, sdk.ErrUnsupportedContentType
	}
	part := message.Parts[0]
	if part.Filename != "" || len(part.Metadata) > 0 {
		return messageContent{}, sdk.ErrUnsupportedContentType
	}
	var raw []byte
	switch value := part.Content.(type) {
	case sdk.Text:
		switch part.MediaType {
		case "", mediaTypePlainText:
			payload, err := run.EncodePayload(string(value))
			return messageContent{payload: payload, mediaType: mediaTypePlainText}, err
		case mediaTypeJSON:
			raw = []byte(value)
		default:
			return messageContent{}, sdk.ErrUnsupportedContentType
		}
	case sdk.Raw:
		if part.MediaType != mediaTypeJSON {
			return messageContent{}, sdk.ErrUnsupportedContentType
		}
		raw = []byte(value)
	case sdk.Data:
		if part.MediaType != "" && part.MediaType != mediaTypeJSON || !exactData(value.Value) {
			return messageContent{}, sdk.ErrUnsupportedContentType
		}
		payload, err := run.EncodePayload(value.Value)
		return messageContent{payload: payload, mediaType: mediaTypeJSON}, err
	default:
		return messageContent{}, sdk.ErrUnsupportedContentType
	}
	payload, err := run.ParsePayload(raw)
	if err != nil {
		return messageContent{}, fmt.Errorf("%w: malformed JSON part", sdk.ErrInvalidParams)
	}
	return messageContent{payload: payload, mediaType: mediaTypeJSON}, nil
}

func exactData(value any) bool {
	switch v := value.(type) {
	case nil, bool, string, json.Number:
		return true
	case []any:
		for _, item := range v {
			if !exactData(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range v {
			if !exactData(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (c contentPolicy) part(raw json.RawMessage) *sdk.Part {
	var text *string
	if slices.Contains(c.outputs, mediaTypePlainText) && jsonv2.Unmarshal(raw, &text) == nil && text != nil {
		part := sdk.NewTextPart(*text)
		part.MediaType = mediaTypePlainText
		return part
	}
	part := sdk.NewTextPart(string(raw))
	part.MediaType = mediaTypePlainText
	if slices.Contains(c.outputs, mediaTypeJSON) {
		part.MediaType = mediaTypeJSON
	}
	return part
}

func sameModes(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func validateHistoryLength(length *int) error {
	if length == nil || *length == 0 {
		return nil
	}
	if *length < 0 {
		return sdk.ErrInvalidParams
	}
	return sdk.ErrUnsupportedOperation
}
