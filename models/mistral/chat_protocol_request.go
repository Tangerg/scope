package mistral

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"slices"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

const (
	mediaTypeImagePrefix = "image/"
	mediaTypeAudioPrefix = "audio/"
	mediaTypePDF         = "application/pdf"
	dataURIPrefix        = "data:"
	dataURIBase64Marker  = ";base64,"
	emptyJSONObject      = "{}"
)

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
				content, ok := toolResult.Output.Text()
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
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil {
		return nil, fmt.Errorf("media MIME %q: %w", value.MIME, err)
	}
	switch {
	case strings.HasPrefix(mediaType, mediaTypeImagePrefix):
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
	case strings.HasPrefix(mediaType, mediaTypeAudioPrefix):
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
		data, err := value.Bytes()
		if err != nil {
			return "", err
		}
		return dataURIPrefix + value.MIME + dataURIBase64Marker + base64.StdEncoding.EncodeToString(data), nil
	default:
		return "", fmt.Errorf("media source %q is unsupported", value.Source.Kind)
	}
}

func mapMistralAssistantMessage(parts []corechat.Part) (chatMessage, error) {
	message := chatMessage{Role: chatRoleAssistant}
	content := make([]any, 0, len(parts))
	for partIndex := range parts {
		part := parts[partIndex]
		switch part.Kind {
		case corechat.PartText:
			content = append(content, textChunk{Type: contentTypeText, Text: part.Text})
		case corechat.PartRefusal:
			content = append(content, textChunk{Type: contentTypeText, Text: part.Text})
		case corechat.PartReasoning:
			frames, framed, err := decodeThinkingFrames(part.ReasoningState)
			if err != nil {
				return chatMessage{}, fmt.Errorf("parts[%d].reasoning signature: %w", partIndex, err)
			}
			if framed {
				chunk, err := coalesceThinkingFrames(frames)
				if err != nil {
					return chatMessage{}, fmt.Errorf("parts[%d].reasoning signature: %w", partIndex, err)
				}
				content = append(content, chunk)
				continue
			}
			if part.Text != "" {
				content = append(content, thinkChunk{
					Type:     contentTypeThinking,
					Thinking: []textChunk{{Type: contentTypeText, Text: part.Text}},
					Closed:   true,
				})
			}
		case corechat.PartToolCall:
			arguments := json.RawMessage(part.ToolCall.Arguments)
			if len(arguments) == 0 {
				arguments = json.RawMessage(emptyJSONObject)
			}
			if !json.Valid(arguments) {
				return chatMessage{}, fmt.Errorf("parts[%d].tool_call.arguments contains invalid JSON", partIndex)
			}
			message.ToolCalls = append(message.ToolCalls, chatToolCall{
				ID:   part.ToolCall.ID,
				Type: toolTypeFunction,
				Function: chatFunctionCall{
					Name:      part.ToolCall.Name,
					Arguments: arguments,
				},
			})
		default:
			return chatMessage{}, fmt.Errorf("parts[%d]: unsupported assistant part %q", partIndex, part.Kind)
		}
	}
	if len(content) == 1 {
		if text, ok := content[0].(textChunk); ok {
			message.Content = text.Text
		} else {
			message.Content = content
		}
	} else if len(content) > 0 {
		message.Content = content
	}
	return message, nil
}

func mapChatTools(definitions []corechat.ToolDefinition) ([]chatTool, error) {
	tools := make([]chatTool, 0, len(definitions))
	for index := range definitions {
		var parameters map[string]any
		if err := json.Unmarshal(definitions[index].InputSchema, &parameters); err != nil {
			return nil, fmt.Errorf("mistral: tools[%d].input_schema: %w", index, err)
		}
		tools = append(tools, chatTool{
			Type: toolTypeFunction,
			Function: chatFunction{
				Name:        definitions[index].Name,
				Description: definitions[index].Description,
				Parameters:  parameters,
			},
		})
	}
	return tools, nil
}

func mapMistralToolChoice(choice *corechat.ToolChoice) (toolChoice, *bool, error) {
	if choice == nil {
		return "", nil, nil
	}
	var mode toolChoice
	switch choice.Mode {
	case corechat.ToolChoiceAuto:
		mode = toolChoiceAuto
	case corechat.ToolChoiceNone:
		mode = toolChoiceNone
	case corechat.ToolChoiceRequired:
		mode = toolChoiceAny
	case corechat.ToolChoiceNamed:
		return "", nil, errors.New("mistral: named tool choice is not supported")
	default:
		return "", nil, fmt.Errorf("mistral: unsupported tool choice mode %q", choice.Mode)
	}
	var parallel *bool
	switch choice.Parallelism {
	case "":
	case corechat.ToolParallelismAllow:
		parallel = new(true)
	case corechat.ToolParallelismSingle:
		parallel = new(false)
	default:
		return "", nil, fmt.Errorf("mistral: unsupported tool parallelism %q", choice.Parallelism)
	}
	return mode, parallel, nil
}
