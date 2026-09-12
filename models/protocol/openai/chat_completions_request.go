package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func mapToolDefinitions(definitions []corechat.ToolDefinition) ([]openaisdk.ChatCompletionToolUnionParam, error) {
	tools := make([]openaisdk.ChatCompletionToolUnionParam, 0, len(definitions))
	for i := range definitions {
		var schema map[string]any
		if err := json.Unmarshal(definitions[i].InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("openai: tools[%d].input_schema: %w", i, err)
		}
		tools = append(tools, openaisdk.ChatCompletionToolUnionParam{
			OfFunction: &openaisdk.ChatCompletionFunctionToolParam{
				Function: openaisdk.FunctionDefinitionParam{
					Name:        definitions[i].Name,
					Description: openaisdk.String(definitions[i].Description),
					Parameters:  schema,
				},
			},
		})
	}
	return tools, nil
}

func mapRequestMessages(messages []corechat.Message) ([]openaisdk.ChatCompletionMessageParamUnion, error) {
	result := make([]openaisdk.ChatCompletionMessageParamUnion, 0, len(messages))
	for i := range messages {
		mapped, err := mapRequestMessage(messages[i])
		if err != nil {
			return nil, fmt.Errorf("openai: messages[%d]: %w", i, err)
		}
		result = append(result, mapped...)
	}
	return result, nil
}

func mapRequestMessage(message corechat.Message) ([]openaisdk.ChatCompletionMessageParamUnion, error) {
	switch message.Role {
	case corechat.RoleSystem:
		return []openaisdk.ChatCompletionMessageParamUnion{openaisdk.SystemMessage(message.Text())}, nil
	case corechat.RoleUser:
		mapped, err := mapUserMessage(message)
		return []openaisdk.ChatCompletionMessageParamUnion{mapped}, err
	case corechat.RoleAssistant:
		mapped, err := mapAssistantMessage(message)
		return []openaisdk.ChatCompletionMessageParamUnion{mapped}, err
	case corechat.RoleTool:
		mapped := make([]openaisdk.ChatCompletionMessageParamUnion, 0, len(message.Parts))
		for i := range message.Parts {
			result := message.Parts[i].ToolResult
			if result == nil {
				return nil, fmt.Errorf("parts[%d]: missing tool result", i)
			}
			text, ok := result.Output.Text()
			if !ok {
				return nil, fmt.Errorf("parts[%d]: OpenAI Chat Completions does not support media Tool output", i)
			}
			mapped = append(mapped, openaisdk.ToolMessage(text, result.ID))
		}
		return mapped, nil
	default:
		return nil, fmt.Errorf("unsupported role %q", message.Role)
	}
}

func mapUserMessage(message corechat.Message) (openaisdk.ChatCompletionMessageParamUnion, error) {
	if len(message.Parts) == 1 && message.Parts[0].Kind == corechat.PartText {
		return openaisdk.UserMessage(message.Parts[0].Text), nil
	}

	parts := make([]openaisdk.ChatCompletionContentPartUnionParam, 0, len(message.Parts))
	for i := range message.Parts {
		part := message.Parts[i]
		switch part.Kind {
		case corechat.PartText:
			parts = append(parts, openaisdk.ChatCompletionContentPartUnionParam{
				OfText: &openaisdk.ChatCompletionContentPartTextParam{Text: part.Text},
			})
		case corechat.PartMedia:
			mapped, err := mapUserMedia(part.Media)
			if err != nil {
				return openaisdk.ChatCompletionMessageParamUnion{}, fmt.Errorf("parts[%d]: %w", i, err)
			}
			parts = append(parts, mapped)
		default:
			return openaisdk.ChatCompletionMessageParamUnion{}, fmt.Errorf("parts[%d]: unsupported user part %q", i, part.Kind)
		}
	}
	return openaisdk.UserMessage(parts), nil
}

func mapUserMedia(value *media.Media) (openaisdk.ChatCompletionContentPartUnionParam, error) {
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil {
		return openaisdk.ChatCompletionContentPartUnionParam{}, fmt.Errorf("media MIME %q: %w", value.MIME, err)
	}
	major, subtype, _ := strings.Cut(mediaType, "/")

	switch major {
	case "image":
		location, locationErr := mediaLocation(value)
		if locationErr != nil {
			return openaisdk.ChatCompletionContentPartUnionParam{}, fmt.Errorf("image source: %w", locationErr)
		}
		return openaisdk.ChatCompletionContentPartUnionParam{
			OfImageURL: &openaisdk.ChatCompletionContentPartImageParam{
				ImageURL: openaisdk.ChatCompletionContentPartImageImageURLParam{URL: location},
			},
		}, nil
	case "audio":
		if value.Source.Kind != media.SourceBytes {
			return openaisdk.ChatCompletionContentPartUnionParam{}, fmt.Errorf("audio requires bytes, got %q", value.Source.Kind)
		}
		if subtype != "wav" && subtype != "mp3" {
			return openaisdk.ChatCompletionContentPartUnionParam{}, fmt.Errorf("audio MIME subtype %q is unsupported", subtype)
		}
		data, bytesErr := value.Bytes()
		if bytesErr != nil {
			return openaisdk.ChatCompletionContentPartUnionParam{}, bytesErr
		}
		return openaisdk.ChatCompletionContentPartUnionParam{
			OfInputAudio: &openaisdk.ChatCompletionContentPartInputAudioParam{
				InputAudio: openaisdk.ChatCompletionContentPartInputAudioInputAudioParam{
					Data:   base64.StdEncoding.EncodeToString(data),
					Format: subtype,
				},
			},
		}, nil
	default:
		file := openaisdk.ChatCompletionContentPartFileFileParam{Filename: openaisdk.String(value.Name)}
		switch value.Source.Kind {
		case media.SourceReference:
			ref, refErr := value.Reference()
			if refErr != nil {
				return openaisdk.ChatCompletionContentPartUnionParam{}, refErr
			}
			file.FileID = openaisdk.String(ref)
		case media.SourceBytes, media.SourceURI:
			location, locationErr := mediaLocation(value)
			if locationErr != nil {
				return openaisdk.ChatCompletionContentPartUnionParam{}, locationErr
			}
			file.FileData = openaisdk.String(location)
		default:
			return openaisdk.ChatCompletionContentPartUnionParam{}, fmt.Errorf("unsupported file source %q", value.Source.Kind)
		}
		return openaisdk.ChatCompletionContentPartUnionParam{
			OfFile: &openaisdk.ChatCompletionContentPartFileParam{File: file},
		}, nil
	}
}

func mediaLocation(value *media.Media) (string, error) {
	switch value.Source.Kind {
	case media.SourceURI:
		return value.URI()
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return "", err
		}
		return "data:" + value.MIME + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	default:
		return "", fmt.Errorf("source %q cannot be represented as a URL", value.Source.Kind)
	}
}

func mapAssistantMessage(message corechat.Message) (openaisdk.ChatCompletionMessageParamUnion, error) {
	mapped := openaisdk.AssistantMessage(message.Text())
	assistant := mapped.OfAssistant
	var audioID string
	for i := range message.Parts {
		part := message.Parts[i]
		switch part.Kind {
		case corechat.PartText:
			// Text was flattened in order by Message.Text above.
		case corechat.PartToolCall:
			assistant.ToolCalls = append(assistant.ToolCalls, openaisdk.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openaisdk.ChatCompletionMessageFunctionToolCallParam{
					ID: part.ToolCall.ID,
					Function: openaisdk.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      part.ToolCall.Name,
						Arguments: part.ToolCall.Arguments,
					},
				},
			})
		case corechat.PartReasoning:
			// Reasoning is provider-owned replay state. The selected dialect
			// decides whether and how it is represented on the wire.
		case corechat.PartMedia:
			if audioID != "" {
				return openaisdk.ChatCompletionMessageParamUnion{}, fmt.Errorf("parts[%d]: Chat Completions supports at most one assistant audio part", i)
			}
			if part.Media.Source.Kind != media.SourceReference || part.Media.Source.Ref == "" {
				return openaisdk.ChatCompletionMessageParamUnion{}, fmt.Errorf("parts[%d]: assistant audio replay requires a provider reference", i)
			}
			audioID = part.Media.Source.Ref
		case corechat.PartRefusal:
			if assistant.Refusal.Valid() {
				return openaisdk.ChatCompletionMessageParamUnion{}, fmt.Errorf("parts[%d]: Chat Completions supports at most one refusal part", i)
			}
			assistant.Refusal = openaisdk.String(part.Text)
		default:
			return openaisdk.ChatCompletionMessageParamUnion{}, fmt.Errorf("parts[%d]: unsupported assistant part %q", i, part.Kind)
		}
	}

	if audioID != "" {
		assistant.Audio.ID = audioID
	}
	return mapped, nil
}
