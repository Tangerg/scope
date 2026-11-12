package mistral

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

const (
	mediaTypePDF    = "application/pdf"
	emptyJSONObject = "{}"
)

func newResponseFormat(format *corechat.OutputFormat) (*responseFormat, error) {
	if format == nil {
		return nil, nil
	}
	switch format.Type {
	case corechat.OutputFormatText:
		return &responseFormat{Type: outputFormatTypeText}, nil
	case corechat.OutputFormatJSON:
		return &responseFormat{Type: outputFormatTypeJSONObject}, nil
	case corechat.OutputFormatJSONSchema:
		return &responseFormat{
			Type: outputFormatTypeJSONSchema,
			JSONSchema: &jsonSchemaDefinition{
				Name: format.Name, Description: format.Description,
				Schema: bytes.Clone(format.Schema), Strict: true,
			},
		}, nil
	default:
		return nil, fmt.Errorf("mistral: unsupported output format %q", format.Type)
	}
}

func mapChatRequestMessages(messages []corechat.Message) ([]chatMessage, error) {
	result := make([]chatMessage, 0, len(messages))
	for messageIndex := range messages {
		message := messages[messageIndex]
		switch message.Role {
		case corechat.RoleSystem:
			result = append(result, chatMessage{Role: chatRoleSystem, Content: message.Text()})
		case corechat.RoleUser:
			content, err := mapMistralUserContent(message.Parts)
			if err != nil {
				return nil, fmt.Errorf("mistral: messages[%d]: %w", messageIndex, err)
			}
			result = append(result, chatMessage{Role: chatRoleUser, Content: content})
		case corechat.RoleAssistant:
			mapped, err := mapMistralAssistantMessage(message.Parts)
			if err != nil {
				return nil, fmt.Errorf("mistral: messages[%d]: %w", messageIndex, err)
			}
			result = append(result, mapped)
		case corechat.RoleTool:
			for partIndex := range message.Parts {
				toolResult := message.Parts[partIndex].ToolResult
				if toolResult == nil {
					return nil, fmt.Errorf("mistral: messages[%d].parts[%d]: missing tool result", messageIndex, partIndex)
				}
				content, ok := toolResult.MarkedOutput().Text()
				if !ok {
					return nil, fmt.Errorf("mistral: messages[%d].parts[%d]: media Tool output is unsupported", messageIndex, partIndex)
				}
				result = append(result, chatMessage{
					Role:       chatRoleTool,
					Content:    content,
					ToolCallID: toolResult.ID,
					Name:       toolResult.Name,
				})
			}
		default:
			return nil, fmt.Errorf("mistral: messages[%d]: unsupported role %q", messageIndex, message.Role)
		}
	}
	return result, nil
}

func mapMistralUserContent(parts []corechat.Part) (any, error) {
	if len(parts) == 1 && parts[0].Kind == corechat.PartText {
		return parts[0].Text, nil
	}
	content := make([]any, 0, len(parts))
	for partIndex := range parts {
		part := parts[partIndex]
		switch part.Kind {
		case corechat.PartText:
			content = append(content, textChunk{Type: contentTypeText, Text: part.Text})
		case corechat.PartMedia:
			chunk, err := mapMistralMedia(part.Media)
			if err != nil {
				return nil, fmt.Errorf("parts[%d]: %w", partIndex, err)
			}
			content = append(content, chunk)
		default:
			return nil, fmt.Errorf("parts[%d]: unsupported user part %q", partIndex, part.Kind)
		}
	}
	return content, nil
}

func mapMistralMedia(value *media.Media) (any, error) {
	mediaType := value.Type()
	switch {
	case mediaType.TopLevel() == media.TopLevelImage:
		uri, err := mistralMediaURI(value)
		if err != nil {
			return nil, err
		}
		return imageURLChunk{Type: contentTypeImageURL, ImageURL: imageURLValue(uri)}, nil
	case mediaType == mediaTypePDF:
		if value.Source.Kind != media.SourceURI {
			return nil, errors.New("PDF input requires a URL source")
		}
		uri, err := value.URI()
		if err != nil {
			return nil, err
		}
		return documentURLChunk{Type: contentTypeDocumentURL, DocumentURL: uri, DocumentName: value.Name}, nil
	case mediaType.TopLevel() == media.TopLevelAudio:
		if value.Source.Kind != media.SourceBytes {
			return nil, errors.New("audio input requires a byte source")
		}
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		return audioChunk{Type: contentTypeInputAudio, InputAudio: base64.StdEncoding.EncodeToString(data)}, nil
	default:
		return nil, fmt.Errorf("media MIME %q is unsupported", mediaType)
	}
}

func mistralMediaURI(value *media.Media) (string, error) {
	switch value.Source.Kind {
	case media.SourceURI:
		return value.URI()
	case media.SourceBytes:
		return value.DataURI()
	default:
		return "", fmt.Errorf("media source %q is unsupported", value.Source.Kind)
	}
}

func mapMistralAssistantMessage(parts []corechat.Part) (chatMessage, error) {
	if len(parts) == 0 {
		return chatMessage{}, errors.New("assistant metadata has no replayable Mistral content")
	}
	message := chatMessage{Role: chatRoleAssistant}
	var content assistantContent
	for partIndex, part := range parts {
		if part.Kind != corechat.PartReasoning {
			if err := content.flushThinking(); err != nil {
				return chatMessage{}, err
			}
		}
		switch part.Kind {
		case corechat.PartText, corechat.PartRefusal:
			content.chunks = append(content.chunks, textChunk{Type: contentTypeText, Text: part.Text})
		case corechat.PartReasoning:
			if err := content.addReasoning(part); err != nil {
				return chatMessage{}, fmt.Errorf("parts[%d].reasoning %w", partIndex, err)
			}
		case corechat.PartToolCall:
			call, err := mapAssistantToolCall(part.ToolCall)
			if err != nil {
				return chatMessage{}, fmt.Errorf("parts[%d].tool_call.arguments: %w", partIndex, err)
			}
			message.ToolCalls = append(message.ToolCalls, call)
		default:
			return chatMessage{}, fmt.Errorf("parts[%d]: unsupported assistant part %q", partIndex, part.Kind)
		}
	}
	if err := content.flushThinking(); err != nil {
		return chatMessage{}, err
	}
	message.Content = content.value()
	return message, nil
}

// assistantContent collects replayed content chunks. Framed reasoning parts of
// one thinking block accumulate in thinking until the block closes, a part of
// another block or kind arrives, or the message ends.
type assistantContent struct {
	chunks   []any
	thinking *thinkingReplay
}

func (a *assistantContent) flushThinking() error {
	if a.thinking == nil {
		return nil
	}
	chunk, err := a.thinking.chunk()
	if err != nil {
		return err
	}
	a.chunks = append(a.chunks, chunk)
	a.thinking = nil
	return nil
}

// addReasoning replays a framed part into its thinking block; an unframed
// part is a closed thinking chunk of its own.
func (a *assistantContent) addReasoning(part corechat.Part) error {
	state, framed, err := decodeThinkingFrame(part.ReasoningState)
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	if !framed {
		if err := a.flushThinking(); err != nil {
			return err
		}
		if part.Text != "" {
			a.chunks = append(a.chunks, thinkChunk{Type: contentTypeThinking, Thinking: []textChunk{{Type: contentTypeText, Text: part.Text}}, Closed: true})
		}
		return nil
	}
	if a.thinking != nil && a.thinking.block != state.Block {
		if err := a.flushThinking(); err != nil {
			return err
		}
	}
	if a.thinking == nil {
		a.thinking = &thinkingReplay{block: state.Block}
	}
	if err := a.thinking.add(state, part.Text); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if state.closed() {
		return a.flushThinking()
	}
	return nil
}

// value is a plain string for a single text chunk, as Mistral replays it.
func (a *assistantContent) value() any {
	if len(a.chunks) == 1 {
		if text, ok := a.chunks[0].(textChunk); ok {
			return text.Text
		}
	}
	if len(a.chunks) == 0 {
		return nil
	}
	return a.chunks
}

func mapAssistantToolCall(call *corechat.ToolCall) (chatToolCall, error) {
	arguments := json.RawMessage(call.Arguments)
	if len(arguments) == 0 {
		arguments = json.RawMessage(emptyJSONObject)
	}
	if !jsontext.Value(arguments).IsValid() {
		return chatToolCall{}, errors.New("contains invalid JSON")
	}
	return chatToolCall{ID: call.ID, Type: toolTypeFunction, Function: chatFunctionCall{Name: call.Name, Arguments: arguments}}, nil
}

func mapChatTools(definitions []toolcontract.Definition) ([]chatTool, error) {
	tools := make([]chatTool, 0, len(definitions))
	for index := range definitions {
		var parameters map[string]json.RawMessage
		if err := jsonv2.Unmarshal(definitions[index].InputSchema, &parameters); err != nil {
			return nil, fmt.Errorf("mistral: tools[%d].input_schema: %w", index, err)
		}
		tools = append(tools, chatTool{
			Type: toolTypeFunction,
			Function: chatFunction{
				Name:        definitions[index].Name,
				Description: definitions[index].Description,
				Parameters:  json.RawMessage(bytes.Clone(definitions[index].InputSchema)),
			},
		})
	}
	return tools, nil
}

func mapMistralToolChoice(choice *corechat.ToolChoice) (*toolChoice, *bool, error) {
	if choice == nil {
		return nil, nil, nil
	}
	mapped := &toolChoice{}
	switch choice.Mode {
	case corechat.ToolChoiceAuto:
		mapped.mode = toolChoiceAuto
	case corechat.ToolChoiceNone:
		mapped.mode = toolChoiceNone
	case corechat.ToolChoiceRequired:
		mapped.mode = toolChoiceAny
	case corechat.ToolChoiceNamed:
		mapped.name = choice.Name
	default:
		return nil, nil, fmt.Errorf("mistral: unsupported tool choice mode %q", choice.Mode)
	}
	var parallel *bool
	switch choice.Parallelism {
	case "":
	case corechat.ToolParallelismAllow:
		parallel = new(true)
	case corechat.ToolParallelismSingle:
		parallel = new(false)
	default:
		return nil, nil, fmt.Errorf("mistral: unsupported tool parallelism %q", choice.Parallelism)
	}
	return mapped, parallel, nil
}
