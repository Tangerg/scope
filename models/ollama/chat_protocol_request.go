package ollama

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

const (
	// RequestExtensionKey stores official Ollama /api/chat request fields that
	// have no provider-neutral Core equivalent. Unknown and Core-owned fields are
	// rejected; options remains an open native map. Think belongs exclusively here
	// because it admits both booleans and levels.
	RequestExtensionKey = Provider + "/request"

	protocolGeneratedToolPrefix = "ollama/generated/"
	maximumTopLogprobs          = 20

	nativeFieldModel             = "model"
	nativeFieldMessages          = "messages"
	nativeFieldStream            = "stream"
	nativeFieldTools             = "tools"
	nativeFieldFormat            = "format"
	nativeFieldOptions           = "options"
	nativeFieldToolChoice        = "tool_choice"
	nativeFieldParallelToolCalls = "parallel_tool_calls"

	nativeOptionFrequencyPenalty = "frequency_penalty"
	nativeOptionNumPredict       = "num_predict"
	nativeOptionPresencePenalty  = "presence_penalty"
	nativeOptionStop             = "stop"
	nativeOptionTemperature      = "temperature"
	nativeOptionTopK             = "top_k"
	nativeOptionTopP             = "top_p"
)

func mapProtocolRequest(defaults corechat.Options, req *corechat.Request) (*nativeChatRequest, error) {
	options, err := defaults.Resolve(req.Options)
	if err != nil {
		return nil, fmt.Errorf("ollama: options: %w", err)
	}
	resolvedRequest := *req
	resolvedRequest.Options = options
	req = &resolvedRequest

	if options.Model == "" {
		return nil, errors.New("ollama: model is required in defaults or request options")
	}
	if req.ToolChoice != nil {
		return nil, errors.New("ollama: tool choice is not supported by the native chat API")
	}
	if options.ReasoningEffort != "" {
		return nil, errors.New("ollama: options.reasoning_effort is unsupported by native chat; set think through the ollama/request extension")
	}

	apiReq, err := decodeProtocolRequestExtension(req)
	if err != nil {
		return nil, err
	}
	apiReq.Model = options.Model
	apiReq.Stream = new(true)
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
		apiReq.Options = make(map[string]json.RawMessage)
	}
	if err := applyCoreOptions(apiReq.Options, options); err != nil {
		return nil, err
	}
	if apiReq.TopLogprobs < 0 || apiReq.TopLogprobs > maximumTopLogprobs {
		return nil, fmt.Errorf("ollama: top_logprobs must be between 0 and %d", maximumTopLogprobs)
	}
	return apiReq, nil
}

// applyCoreOptions merges Core-owned options into the open map that carries
// the caller's native extension values. Encoding each to its own JSON token
// keeps the map lossless, so an extension integer never rounds through float64.
func applyCoreOptions(native map[string]json.RawMessage, options corechat.Options) error {
	values, err := coreOptionValues(options)
	if err != nil {
		return err
	}
	for _, option := range values {
		encoded, err := jsonv2.Marshal(option.value)
		if err != nil {
			return fmt.Errorf("ollama: encode options.%s: %w", option.key, err)
		}
		native[option.key] = encoded
	}
	return nil
}

type nativeOption struct {
	key   string
	value any
}

// coreOptionValues lists the native value of every set Core option.
func coreOptionValues(options corechat.Options) ([]nativeOption, error) {
	var values []nativeOption
	if options.FrequencyPenalty != nil {
		values = append(values, nativeOption{nativeOptionFrequencyPenalty, float32(*options.FrequencyPenalty)})
	}
	if options.MaxOutputTokens != nil {
		value, err := protocolInt(nativeOptionNumPredict, *options.MaxOutputTokens)
		if err != nil {
			return nil, err
		}
		values = append(values, nativeOption{nativeOptionNumPredict, value})
	}
	if options.PresencePenalty != nil {
		values = append(values, nativeOption{nativeOptionPresencePenalty, float32(*options.PresencePenalty)})
	}
	if len(options.Stop) > 0 {
		values = append(values, nativeOption{nativeOptionStop, options.Stop})
	}
	if options.Temperature != nil {
		values = append(values, nativeOption{nativeOptionTemperature, float32(*options.Temperature)})
	}
	if options.TopK != nil {
		value, err := protocolInt(nativeOptionTopK, *options.TopK)
		if err != nil {
			return nil, err
		}
		values = append(values, nativeOption{nativeOptionTopK, value})
	}
	if options.TopP != nil {
		values = append(values, nativeOption{nativeOptionTopP, float32(*options.TopP)})
	}
	return values, nil
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
	if err := jsonv2.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("ollama: extension %q: %w", RequestExtensionKey, err)
	}
	if fields == nil {
		return nil, fmt.Errorf("ollama: extension %q must be an object", RequestExtensionKey)
	}
	for field := range fields {
		switch field {
		case nativeFieldModel, nativeFieldMessages, nativeFieldStream, nativeFieldTools:
			return nil, fmt.Errorf("ollama: extension %q field %q is owned by the Core request", RequestExtensionKey, field)
		case nativeFieldFormat:
			return nil, fmt.Errorf("ollama: extension %q field %q is owned by options.output_format", RequestExtensionKey, field)
		case nativeFieldToolChoice, nativeFieldParallelToolCalls:
			return nil, fmt.Errorf("ollama: extension %q field %q is owned by options.tool_choice", RequestExtensionKey, field)
		}
	}
	if rawOptions, exists := fields[nativeFieldOptions]; exists {
		var optionFields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(rawOptions, &optionFields); err != nil {
			return nil, fmt.Errorf("ollama: extension %q field %q: %w", RequestExtensionKey, nativeFieldOptions, err)
		}
		for field := range optionFields {
			switch field {
			case nativeOptionFrequencyPenalty, nativeOptionNumPredict, nativeOptionPresencePenalty, nativeOptionStop, nativeOptionTemperature, nativeOptionTopK, nativeOptionTopP:
				return nil, fmt.Errorf("ollama: extension %q field %q.%s is owned by Core chat options", RequestExtensionKey, nativeFieldOptions, field)
			}
		}
	}
	if err := jsonv2.Unmarshal(raw, apiReq, jsonv2.RejectUnknownMembers(true)); err != nil {
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
			mapped = append(mapped, nativeMessage{Role: nativeRoleSystem, Content: message.Text()})
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
				content, ok := result.MarkedOutput().Text()
				if !ok {
					return nil, fmt.Errorf("ollama: messages[%d].parts[%d]: media Tool output is unsupported", i, j)
				}
				mapped = append(mapped, nativeMessage{
					Role:       nativeRoleTool,
					Content:    content,
					ToolName:   result.Name,
					ToolCallID: result.ID,
				})
			}
		default:
			return nil, fmt.Errorf("ollama: messages[%d]: unsupported role %q", i, message.Role)
		}
	}
	return mapped, nil
}

func mapProtocolUserMessage(message corechat.Message) (nativeMessage, error) {
	mapped := nativeMessage{Role: nativeRoleUser}
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
	if len(message.Parts) == 0 {
		return nativeMessage{}, errors.New("assistant metadata has no replayable Ollama content")
	}
	mapped := nativeMessage{Role: nativeRoleAssistant}
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
			mapped.ToolCalls = append(mapped.ToolCalls, nativeToolCall{
				ID: part.ToolCall.ID,
				Function: nativeToolCallFunction{
					Index:     new(len(mapped.ToolCalls)),
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
	if err := jsonv2.Unmarshal([]byte(arguments), &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return nativeJSONObject{}, err
	}
	var mapped nativeJSONObject
	if err := jsonv2.Unmarshal([]byte(arguments), &mapped); err != nil {
		return nativeJSONObject{}, err
	}
	return mapped, nil
}

func mapProtocolTools(definitions []toolcontract.Definition) (nativeTools, error) {
	if len(definitions) == 0 {
		return nil, nil
	}
	mapped := make(nativeTools, 0, len(definitions))
	for i := range definitions {
		var parameters map[string]any
		if err := jsonv2.Unmarshal(definitions[i].InputSchema, &parameters); err != nil {
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
