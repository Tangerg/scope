package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

const (
	// RequestExtensionKey stores an official [genai.GenerateContentConfig].
	// Core options, messages, and tools take precedence over overlapping fields.
	RequestExtensionKey = "google/request"
)

func mapProtocolRequest(provider string, defaults corechat.Options, req *corechat.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	options, err := defaults.Resolve(req.Options)
	if err != nil {
		return "", nil, nil, fmt.Errorf("google: options: %w", err)
	}
	resolvedRequest := *req
	resolvedRequest.Options = options
	req = &resolvedRequest
	config, err := decodeProtocolConfig(provider, req)
	if err != nil {
		return "", nil, nil, err
	}

	if options.Model == "" {
		return "", nil, nil, errors.New("google: model is required in defaults or request options")
	}
	if options.MaxOutputTokens != nil {
		if *options.MaxOutputTokens > math.MaxInt32 {
			return "", nil, nil, errors.New("google: options.max_output_tokens exceeds int32")
		}
		config.MaxOutputTokens = int32(*options.MaxOutputTokens)
	}
	if options.Temperature != nil {
		value := float32(*options.Temperature)
		config.Temperature = &value
	}
	if options.TopK != nil {
		value := float32(*options.TopK)
		config.TopK = &value
	}
	if options.TopP != nil {
		value := float32(*options.TopP)
		config.TopP = &value
	}
	if options.FrequencyPenalty != nil {
		value := float32(*options.FrequencyPenalty)
		config.FrequencyPenalty = &value
	}
	if options.PresencePenalty != nil {
		value := float32(*options.PresencePenalty)
		config.PresencePenalty = &value
	}
	if len(options.Stop) > 0 {
		config.StopSequences = slices.Clone(options.Stop)
	}
	thinkingLevel, err := mapProtocolThinkingLevel(options.ReasoningEffort)
	if err != nil {
		return "", nil, nil, err
	}
	if thinkingLevel != genai.ThinkingLevelUnspecified {
		if config.ThinkingConfig == nil {
			config.ThinkingConfig = &genai.ThinkingConfig{}
		}
		config.ThinkingConfig.ThinkingLevel = thinkingLevel
	}
	if mapProtocolOutputFormatErr := mapProtocolOutputFormat(options.OutputFormat, config); mapProtocolOutputFormatErr != nil {
		return "", nil, nil, mapProtocolOutputFormatErr
	}
	if toolChoiceErr := mapProtocolToolChoice(req.ToolChoice, config); toolChoiceErr != nil {
		return "", nil, nil, toolChoiceErr
	}

	system, contents, err := mapProtocolMessages(provider, req.Messages)
	if err != nil {
		return "", nil, nil, err
	}
	if system != nil {
		if config.SystemInstruction == nil {
			config.SystemInstruction = system
		} else {
			config.SystemInstruction.Parts = append(config.SystemInstruction.Parts, system.Parts...)
		}
	}
	tools, err := mapProtocolTools(req.Tools)
	if err != nil {
		return "", nil, nil, err
	}
	config.Tools = append(config.Tools, tools...)
	return options.Model, contents, config, nil
}

func mapProtocolOutputFormat(format *corechat.OutputFormat, config *genai.GenerateContentConfig) error {
	if format == nil {
		return nil
	}
	config.ResponseSchema = nil
	config.ResponseJsonSchema = nil
	switch format.Type {
	case corechat.OutputFormatText:
		config.ResponseMIMEType = "text/plain"
	case corechat.OutputFormatJSON:
		config.ResponseMIMEType = "application/json"
	case corechat.OutputFormatJSONSchema:
		schema, err := format.SchemaAs[any]()
		if err != nil {
			return fmt.Errorf("google: output schema: %w", err)
		}
		config.ResponseMIMEType = "application/json"
		config.ResponseJsonSchema = schema
	default:
		return fmt.Errorf("google: unsupported output format %q", format.Type)
	}
	return nil
}

func decodeProtocolConfig(provider string, req *corechat.Request) (*genai.GenerateContentConfig, error) {
	extensionKey := protocolKey(provider, "request")
	raw, found, err := req.Options.Extensions.Decode[json.RawMessage](extensionKey)
	if err != nil {
		return nil, fmt.Errorf("google: extension %q: %w", extensionKey, err)
	}
	if !found {
		return &genai.GenerateContentConfig{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("google: extension %q: %w", extensionKey, err)
	}
	for _, name := range []string{"thinkingConfig", "thinking_config"} {
		value, exists := fields[name]
		if !exists {
			continue
		}
		var thinkingFields map[string]json.RawMessage
		if err := json.Unmarshal(value, &thinkingFields); err != nil {
			return nil, fmt.Errorf("google: extension %q field %q: %w", extensionKey, name, err)
		}
		for _, levelName := range []string{"thinkingLevel", "thinking_level"} {
			if _, exists := thinkingFields[levelName]; exists {
				return nil, fmt.Errorf("google: extension %q field %q.%s is owned by options.reasoning_effort", extensionKey, name, levelName)
			}
		}
	}
	for _, name := range []string{"responseMimeType", "response_mime_type", "responseSchema", "response_schema", "responseJsonSchema", "response_json_schema"} {
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("google: extension %q field %q is owned by options.output_format", extensionKey, name)
		}
	}
	for _, name := range []string{"toolConfig", "tool_config"} {
		value, exists := fields[name]
		if !exists {
			continue
		}
		var toolFields map[string]json.RawMessage
		if err := json.Unmarshal(value, &toolFields); err != nil {
			return nil, fmt.Errorf("google: extension %q field %q: %w", extensionKey, name, err)
		}
		for _, functionName := range []string{"functionCallingConfig", "function_calling_config"} {
			if _, exists := toolFields[functionName]; exists {
				return nil, fmt.Errorf("google: extension %q field %q.%s is owned by options.tool_choice", extensionKey, name, functionName)
			}
		}
	}
	var config genai.GenerateContentConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("google: extension %q: %w", extensionKey, err)
	}
	var aliases struct {
		SafetySettings     []*genai.SafetySetting `json:"safety_settings"`
		ResponseModalities []string               `json:"response_modalities"`
	}
	if err := json.Unmarshal(raw, &aliases); err != nil {
		return nil, fmt.Errorf("google: extension %q aliases: %w", extensionKey, err)
	}
	if len(config.SafetySettings) == 0 && len(aliases.SafetySettings) > 0 {
		config.SafetySettings = aliases.SafetySettings
	}
	if len(config.ResponseModalities) == 0 && len(aliases.ResponseModalities) > 0 {
		config.ResponseModalities = slices.Clone(aliases.ResponseModalities)
	}
	return &config, nil
}

func mapProtocolThinkingLevel(effort corechat.ReasoningEffort) (genai.ThinkingLevel, error) {
	if effort == "" {
		return genai.ThinkingLevelUnspecified, nil
	}
	native := genai.ThinkingLevel(strings.ToUpper(string(effort)))
	switch native {
	case genai.ThinkingLevelMinimal, genai.ThinkingLevelLow, genai.ThinkingLevelMedium, genai.ThinkingLevelHigh:
		return native, nil
	default:
		return genai.ThinkingLevelUnspecified, fmt.Errorf("google: options.reasoning_effort has unsupported value %q", effort)
	}
}

func mapProtocolMessages(provider string, messages []corechat.Message) (*genai.Content, []*genai.Content, error) {
	var systemParts []*genai.Part
	contents := make([]*genai.Content, 0, len(messages))
	for i := range messages {
		message := messages[i]
		switch message.Role {
		case corechat.RoleSystem:
			for j := range message.Parts {
				systemParts = append(systemParts, genai.NewPartFromText(message.Parts[j].Text))
			}
		case corechat.RoleUser:
			parts, err := mapProtocolUserParts(message.Parts)
			if err != nil {
				return nil, nil, fmt.Errorf("google: messages[%d]: %w", i, err)
			}
			contents = append(contents, genai.NewContentFromParts(parts, genai.RoleUser))
		case corechat.RoleAssistant:
			parts, err := mapProtocolAssistantParts(provider, message.Parts)
			if err != nil {
				return nil, nil, fmt.Errorf("google: messages[%d]: %w", i, err)
			}
			contents = append(contents, genai.NewContentFromParts(parts, genai.RoleModel))
		case corechat.RoleTool:
			parts := make([]*genai.Part, 0, len(message.Parts))
			for j := range message.Parts {
				result := message.Parts[j].ToolResult
				id := result.ID
				if strings.HasPrefix(id, protocolGeneratedToolPrefixFor(provider)) {
					id = ""
				}
				response, responseParts, err := protocolToolResult(result.Output, result.IsError)
				if err != nil {
					return nil, nil, fmt.Errorf("google: messages[%d].parts[%d].tool_result.output: %w", i, j, err)
				}
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
					ID:       id,
					Name:     result.Name,
					Response: response,
					Parts:    responseParts,
				}})
			}
			contents = append(contents, genai.NewContentFromParts(parts, genai.RoleUser))
		default:
			return nil, nil, fmt.Errorf("google: messages[%d]: unsupported role %q", i, message.Role)
		}
	}
	var system *genai.Content
	if len(systemParts) > 0 {
		system = genai.NewContentFromParts(systemParts, "")
	}
	return system, contents, nil
}

func mapProtocolUserParts(parts []corechat.Part) ([]*genai.Part, error) {
	mapped := make([]*genai.Part, 0, len(parts))
	for i := range parts {
		switch parts[i].Kind {
		case corechat.PartText:
			mapped = append(mapped, genai.NewPartFromText(parts[i].Text))
		case corechat.PartMedia:
			part, err := mapProtocolMedia(parts[i].Media)
			if err != nil {
				return nil, fmt.Errorf("parts[%d]: %w", i, err)
			}
			mapped = append(mapped, part)
		default:
			return nil, fmt.Errorf("parts[%d]: unsupported user part %q", i, parts[i].Kind)
		}
	}
	return mapped, nil
}

func mapProtocolAssistantParts(provider string, parts []corechat.Part) ([]*genai.Part, error) {
	mapped := make([]*genai.Part, 0, len(parts))
	for i := range parts {
		part := parts[i]
		stateKey := protocolKey(provider, "part_state")
		state, _, err := part.Metadata.Decode[partReplayState](stateKey)
		if err != nil {
			return nil, fmt.Errorf("parts[%d].metadata[%q]: %w", i, stateKey, err)
		}
		switch part.Kind {
		case corechat.PartText:
			mapped = append(mapped, genai.NewPartFromText(part.Text))
		case corechat.PartRefusal:
			mapped = append(mapped, genai.NewPartFromText(part.Text))
		case corechat.PartReasoning:
			mapped = append(mapped, &genai.Part{Text: part.Text, Thought: true, ThoughtSignature: slices.Clone(part.ReasoningState)})
		case corechat.PartToolCall:
			var arguments map[string]any
			if part.ToolCall.Arguments != "" {
				if err := json.Unmarshal([]byte(part.ToolCall.Arguments), &arguments); err != nil {
					return nil, fmt.Errorf("parts[%d].tool_call.arguments: %w", i, err)
				}
			}
			id := part.ToolCall.ID
			if strings.HasPrefix(id, protocolGeneratedToolPrefixFor(provider)) {
				id = ""
			}
			mapped = append(mapped, &genai.Part{FunctionCall: &genai.FunctionCall{
				ID:   id,
				Name: part.ToolCall.Name,
				Args: arguments,
			}})
		case corechat.PartMedia:
			mediaPart, err := mapProtocolMedia(part.Media)
			if err != nil {
				return nil, fmt.Errorf("parts[%d]: %w", i, err)
			}
			mapped = append(mapped, mediaPart)
		default:
			return nil, fmt.Errorf("parts[%d]: unsupported assistant part %q", i, part.Kind)
		}
		state.apply(mapped[len(mapped)-1])
	}
	return mapped, nil
}

func mapProtocolMedia(value *media.Media) (*genai.Part, error) {
	switch value.Source.Kind {
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		part := genai.NewPartFromBytes(data, value.MIME)
		part.InlineData.DisplayName = value.Name
		return part, nil
	case media.SourceURI:
		uri, err := value.URI()
		if err != nil {
			return nil, err
		}
		part := genai.NewPartFromURI(uri, value.MIME)
		part.FileData.DisplayName = value.Name
		return part, nil
	default:
		return nil, fmt.Errorf("media source %q is unsupported", value.Source.Kind)
	}
}

func protocolToolResult(output corechat.ToolOutput, isError bool) (map[string]any, []*genai.FunctionResponsePart, error) {
	if len(output.Content) == 0 {
		return protocolToolResultJSON(output.Details, isError), nil, nil
	}
	var text strings.Builder
	parts := make([]*genai.FunctionResponsePart, 0, len(output.Content))
	for index := range output.Content {
		part := output.Content[index]
		switch part.Kind {
		case corechat.PartText:
			text.WriteString(part.Text)
		case corechat.PartMedia:
			mapped, err := mapProtocolToolMedia(part.Media)
			if err != nil {
				return nil, nil, fmt.Errorf("content[%d]: %w", index, err)
			}
			parts = append(parts, mapped)
		default:
			return nil, nil, fmt.Errorf("content[%d]: unsupported part %q", index, part.Kind)
		}
	}
	return protocolToolResultJSON([]byte(text.String()), isError), parts, nil
}

func mapProtocolToolMedia(value *media.Media) (*genai.FunctionResponsePart, error) {
	switch value.Source.Kind {
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		part := genai.NewFunctionResponsePartFromBytes(data, value.MIME)
		part.InlineData.DisplayName = value.Name
		return part, nil
	case media.SourceURI:
		uri, err := value.URI()
		if err != nil {
			return nil, err
		}
		part := genai.NewFunctionResponsePartFromURI(uri, value.MIME)
		part.FileData.DisplayName = value.Name
		return part, nil
	default:
		return nil, fmt.Errorf("media source %q is unsupported", value.Source.Kind)
	}
}

func protocolToolResultJSON(result []byte, isError bool) map[string]any {
	var decoded any
	if len(result) != 0 && json.Unmarshal(result, &decoded) == nil {
		if !isError {
			if object, ok := decoded.(map[string]any); ok {
				return object
			}
			return map[string]any{"output": decoded}
		}
		return map[string]any{"error": decoded}
	}
	if isError {
		return map[string]any{"error": string(result)}
	}
	return map[string]any{"output": string(result)}
}

func mapProtocolTools(definitions []corechat.ToolDefinition) ([]*genai.Tool, error) {
	if len(definitions) == 0 {
		return nil, nil
	}
	declarations := make([]*genai.FunctionDeclaration, 0, len(definitions))
	for i := range definitions {
		var schema map[string]any
		if err := json.Unmarshal(definitions[i].InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("google: tools[%d].input_schema: %w", i, err)
		}
		declarations = append(declarations, &genai.FunctionDeclaration{
			Name:                 definitions[i].Name,
			Description:          definitions[i].Description,
			ParametersJsonSchema: schema,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: declarations}}, nil
}

func mapProtocolToolChoice(choice *corechat.ToolChoice, config *genai.GenerateContentConfig) error {
	if choice == nil {
		return nil
	}
	if choice.Parallelism == corechat.ToolParallelismSingle {
		return errors.New("google: single tool-call parallelism is not supported")
	}
	functionCalling := &genai.FunctionCallingConfig{}
	switch choice.Mode {
	case corechat.ToolChoiceAuto:
		functionCalling.Mode = genai.FunctionCallingConfigModeAuto
	case corechat.ToolChoiceNone:
		functionCalling.Mode = genai.FunctionCallingConfigModeNone
	case corechat.ToolChoiceRequired:
		functionCalling.Mode = genai.FunctionCallingConfigModeAny
	case corechat.ToolChoiceNamed:
		functionCalling.Mode = genai.FunctionCallingConfigModeAny
		functionCalling.AllowedFunctionNames = []string{choice.Name}
	default:
		return fmt.Errorf("google: unsupported tool choice mode %q", choice.Mode)
	}
	if config.ToolConfig == nil {
		config.ToolConfig = &genai.ToolConfig{}
	}
	config.ToolConfig.FunctionCallingConfig = functionCalling
	return nil
}
