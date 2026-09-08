package ollama

import (
	"bytes"
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
	// RequestExtensionKey stores official Ollama /api/chat request fields that
	// have no provider-neutral Core equivalent. Core model, messages, tools,
	// streaming, and common options take precedence.
	RequestExtensionKey = "ollama/request"

	protocolGeneratedToolPrefix = "ollama/generated/"
)

func mapProtocolRequest(defaults corechat.Options, req *corechat.Request, stream bool) (*nativeChatRequest, error) {
	options, err := defaults.Resolve(req.Options)
	if err != nil {
		return nil, fmt.Errorf("ollama: options: %w", err)
	}
	if options.Model == "" {
		return nil, errors.New("ollama: model is required in defaults or request options")
	}
	if req.ToolChoice != nil {
		return nil, errors.New("ollama: tool choice is not supported by the native chat API")
	}

	apiReq, err := decodeProtocolRequestExtension(req)
	if err != nil {
		return nil, err
	}
	apiReq.Model = options.Model
	apiReq.Stream = &stream
	apiReq.Format, err = mapProtocolOutputFormat(options.OutputFormat)
	if err != nil {
		return nil, err
	}
	apiReq.Messages, err = mapProtocolMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	apiReq.Tools, err = mapProtocolTools(req.Tools)
	if err != nil {
		return nil, err
	}
	if apiReq.Options == nil {
		apiReq.Options = make(map[string]any)
	}
	if options.FrequencyPenalty != nil {
		apiReq.Options["frequency_penalty"] = float32(*options.FrequencyPenalty)
	}
	if options.MaxOutputTokens != nil {
		value, err := protocolInt("max_tokens", *options.MaxOutputTokens)
		if err != nil {
			return nil, err
		}
		apiReq.Options["num_predict"] = value
	}
	if options.PresencePenalty != nil {
		apiReq.Options["presence_penalty"] = float32(*options.PresencePenalty)
	}
	if len(options.Stop) > 0 {
		apiReq.Options["stop"] = slices.Clone(options.Stop)
	}
	if options.Temperature != nil {
		apiReq.Options["temperature"] = float32(*options.Temperature)
	}
	if options.TopK != nil {
		value, err := protocolInt("top_k", *options.TopK)
		if err != nil {
			return nil, err
		}
		apiReq.Options["top_k"] = value
	}
	if options.TopP != nil {
		apiReq.Options["top_p"] = float32(*options.TopP)
	}
	// A reasoning effort names a thinking level, which is exactly what
	// /api/chat's think parameter accepts, so the portable option reaches the
	// daemon rather than being dropped on the way. Every other option above
	// overwrites whatever the native extension set; an empty effort does not,
	// because empty means "the model's default" and a caller who set think
	// natively has already chosen something else — including the boolean form,
	// which no portable effort can express.
	if options.ReasoningEffort != "" {
		think, err := newNativeThinkLevel(string(options.ReasoningEffort))
		if err != nil {
			return nil, err
		}
		apiReq.Think = think
	}
	if apiReq.TopLogprobs < 0 || apiReq.TopLogprobs > 20 {
		return nil, errors.New("ollama: top_logprobs must be between 0 and 20")
	}
	return apiReq, nil
}

func mapProtocolOutputFormat(format *corechat.OutputFormat) (json.RawMessage, error) {
	if format == nil || format.Type == corechat.OutputFormatText {
		return nil, nil
	}
	switch format.Type {
	case corechat.OutputFormatJSON:
		return json.RawMessage(`"json"`), nil
	case corechat.OutputFormatJSONSchema:
		return bytes.Clone(format.Schema), nil
	default:
		return nil, fmt.Errorf("ollama: unsupported output format %q", format.Type)
	}
}

func decodeProtocolRequestExtension(req *corechat.Request) (*nativeChatRequest, error) {
	apiReq := new(nativeChatRequest)
	raw, found, err := req.Options.Extensions.Decode[json.RawMessage](RequestExtensionKey)
	if err != nil {
		return nil, fmt.Errorf("ollama: extension %q: %w", RequestExtensionKey, err)
	}
	if !found {
		return apiReq, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("ollama: extension %q: %w", RequestExtensionKey, err)
	}
	for field := range fields {
		switch field {
		case "model", "messages", "stream", "tools":
			return nil, fmt.Errorf("ollama: extension %q field %q is owned by the Core request", RequestExtensionKey, field)
		case "format":
			return nil, fmt.Errorf("ollama: extension %q field %q is owned by options.output_format", RequestExtensionKey, field)
		case "tool_choice", "parallel_tool_calls":
			return nil, fmt.Errorf("ollama: extension %q field %q is owned by options.tool_choice", RequestExtensionKey, field)
		}
	}
	if rawOptions, exists := fields["options"]; exists {
		var optionFields map[string]json.RawMessage
		if err := json.Unmarshal(rawOptions, &optionFields); err != nil {
			return nil, fmt.Errorf("ollama: extension %q field %q: %w", RequestExtensionKey, "options", err)
		}
		for field := range optionFields {
			switch field {
			case "frequency_penalty", "num_predict", "presence_penalty", "stop", "temperature", "top_k", "top_p":
				return nil, fmt.Errorf("ollama: extension %q field %q.%s is owned by Core chat options", RequestExtensionKey, "options", field)
			}
		}
	}
	if err := json.Unmarshal(raw, apiReq); err != nil {
		return nil, fmt.Errorf("ollama: extension %q: %w", RequestExtensionKey, err)
	}
	return apiReq, nil
}

func protocolInt(name string, value int64) (int, error) {
	maxInt := int64(int(^uint(0) >> 1))
	if value > maxInt {
		return 0, fmt.Errorf("ollama: options.%s exceeds int", name)
	}
	return int(value), nil
}

func mapProtocolMessages(messages []corechat.Message) ([]nativeMessage, error) {
	mapped := make([]nativeMessage, 0, len(messages))
	for i := range messages {
		message := messages[i]
		switch message.Role {
		case corechat.RoleSystem:
			mapped = append(mapped, nativeMessage{Role: "system", Content: message.Text()})
		case corechat.RoleUser:
			user, err := mapProtocolUserMessage(message)
			if err != nil {
				return nil, fmt.Errorf("ollama: messages[%d]: %w", i, err)
			}
			mapped = append(mapped, user)
		case corechat.RoleAssistant:
			assistant, err := mapProtocolAssistantMessage(message)
			if err != nil {
				return nil, fmt.Errorf("ollama: messages[%d]: %w", i, err)
			}
			mapped = append(mapped, assistant)
		case corechat.RoleTool:
			for j := range message.Parts {
				result := message.Parts[j].ToolResult
				content, ok := result.Output.Text()
				if !ok {
					return nil, fmt.Errorf("ollama: messages[%d].parts[%d]: media Tool output is unsupported", i, j)
				}
				id := result.ID
				if strings.HasPrefix(id, protocolGeneratedToolPrefix) {
					id = ""
				}
				mapped = append(mapped, nativeMessage{
					Role:       "tool",
					Content:    content,
					ToolName:   result.Name,
					ToolCallID: id,
				})
			}
		default:
			return nil, fmt.Errorf("ollama: messages[%d]: unsupported role %q", i, message.Role)
		}
	}
	return mapped, nil
}

func mapProtocolUserMessage(message corechat.Message) (nativeMessage, error) {
	mapped := nativeMessage{Role: "user"}
	var text strings.Builder
	for i := range message.Parts {
		part := message.Parts[i]
		switch part.Kind {
		case corechat.PartText:
			text.WriteString(part.Text)
		case corechat.PartMedia:
			image, err := mapProtocolImage(part.Media)
			if err != nil {
				return nativeMessage{}, fmt.Errorf("parts[%d]: %w", i, err)
			}
			mapped.Images = append(mapped.Images, image)
		default:
			return nativeMessage{}, fmt.Errorf("parts[%d]: unsupported user part %q", i, part.Kind)
		}
	}
	mapped.Content = text.String()
	return mapped, nil
}

func mapProtocolImage(value *media.Media) (nativeImageData, error) {
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil || !strings.HasPrefix(mediaType, "image/") {
		return nil, fmt.Errorf("media MIME %q is not an image", value.MIME)
	}
	if value.Source.Kind != media.SourceBytes {
		return nil, fmt.Errorf("image source %q is unsupported; Ollama requires bytes", value.Source.Kind)
	}
	data, err := value.Bytes()
	if err != nil {
		return nil, err
	}
	return nativeImageData(data), nil
}

func mapProtocolAssistantMessage(message corechat.Message) (nativeMessage, error) {
	mapped := nativeMessage{Role: "assistant"}
	var text, reasoning strings.Builder
	for i := range message.Parts {
		part := message.Parts[i]
		switch part.Kind {
		case corechat.PartText:
			text.WriteString(part.Text)
		case corechat.PartRefusal:
			text.WriteString(part.Text)
		case corechat.PartReasoning:
			if len(part.ReasoningState) > 0 {
				return nativeMessage{}, fmt.Errorf("parts[%d]: reasoning signature is unsupported", i)
			}
			reasoning.WriteString(part.Text)
		case corechat.PartToolCall:
			arguments, err := mapProtocolToolArguments(part.ToolCall.Arguments)
			if err != nil {
				return nativeMessage{}, fmt.Errorf("parts[%d].tool_call.arguments: %w", i, err)
			}
			id := part.ToolCall.ID
			if strings.HasPrefix(id, protocolGeneratedToolPrefix) {
				id = ""
			}
			mapped.ToolCalls = append(mapped.ToolCalls, nativeToolCall{
				ID: id,
				Function: nativeToolCallFunction{
					Index:     len(mapped.ToolCalls),
					Name:      part.ToolCall.Name,
					Arguments: arguments,
				},
			})
		case corechat.PartMedia:
			return nativeMessage{}, fmt.Errorf("parts[%d]: assistant media is unsupported", i)
		default:
			return nativeMessage{}, fmt.Errorf("parts[%d]: unsupported assistant part %q", i, part.Kind)
		}
	}
	mapped.Content = text.String()
	mapped.Thinking = reasoning.String()
	return mapped, nil
}

func mapProtocolToolArguments(arguments string) (nativeJSONObject, error) {
	if arguments == "" {
		return emptyNativeJSONObject(), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return nativeJSONObject{}, err
	}
	var mapped nativeJSONObject
	if err := json.Unmarshal([]byte(arguments), &mapped); err != nil {
		return nativeJSONObject{}, err
	}
	return mapped, nil
}

func mapProtocolTools(definitions []corechat.ToolDefinition) (nativeTools, error) {
	if len(definitions) == 0 {
		return nil, nil
	}
	mapped := make(nativeTools, 0, len(definitions))
	for i := range definitions {
		var parameters map[string]any
		if err := json.Unmarshal(definitions[i].InputSchema, &parameters); err != nil {
			return nil, fmt.Errorf("ollama: tools[%d].input_schema: %w", i, err)
		}
		if parameters == nil {
			return nil, fmt.Errorf("ollama: tools[%d].input_schema must be an object", i)
		}
		mapped = append(mapped, nativeTool{
			Type: "function",
			Function: nativeToolFunction{
				Name:        definitions[i].Name,
				Description: definitions[i].Description,
				Parameters:  json.RawMessage(bytes.Clone(definitions[i].InputSchema)),
			},
		})
	}
	return mapped, nil
}
