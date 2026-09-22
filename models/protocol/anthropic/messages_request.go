package anthropic

import (
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"mime"
	"slices"
	"strings"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

const (
	// RequestExtensionKey stores provider-owned top-level Messages API fields in
	// a Core request for fields without a provider-neutral equivalent.
	RequestExtensionKey          = "anthropic/request"
	protocolReasoningKindKey     = "anthropic/reasoning_kind"
	protocolReasoningProviderKey = "anthropic/reasoning_provider"
	protocolReasoningThinking    = "thinking"
	protocolReasoningRedacted    = "redacted_thinking"
	protocolNativeStopReasonKey  = "anthropic/native_stop_reason"
	protocolStopSequenceKey      = "anthropic/stop_sequence"
	protocolUsageKey             = "anthropic/usage"
)

func mapProtocolRequest(defaults corechat.Options, req *corechat.Request, dialect Dialect) (*anthropicsdk.MessageNewParams, error) {
	options, err := defaults.Resolve(req.Options)
	if err != nil {
		return nil, fmt.Errorf("anthropic: options: %w", err)
	}
	resolvedRequest := *req
	resolvedRequest.Options = options
	req = &resolvedRequest
	extensionKey := protocolRequestExtensionKey(dialect.Provider)
	fields, _, err := req.Options.Extensions.Decode[map[string]any](extensionKey)
	if err != nil {
		return nil, fmt.Errorf("anthropic: extension %q: %w", extensionKey, err)
	}
	for _, name := range []string{"model", "messages", "system", "tools", "max_tokens", "temperature", "tool_choice", "top_k", "top_p", "stop_sequences"} {
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("anthropic: extension %q field %q is owned by Core", extensionKey, name)
		}
	}
	outputConfig, err := decodeOutputConfig(fields, extensionKey)
	if err != nil {
		return nil, err
	}
	params := anthropicsdk.MessageNewParams{OutputConfig: outputConfig}
	params.SetExtraFields(fields)

	if options.Model == "" {
		return nil, errors.New("anthropic: model is required in defaults or request options")
	}
	if options.FrequencyPenalty != nil {
		return nil, errors.New("anthropic: options.frequency_penalty is not supported")
	}
	if options.PresencePenalty != nil {
		return nil, errors.New("anthropic: options.presence_penalty is not supported")
	}
	if dialect.RejectTopK && options.TopK != nil {
		return nil, errors.New("anthropic: options.top_k is not supported by current Claude models")
	}
	if dialect.RejectTopP && options.TopP != nil {
		return nil, errors.New("anthropic: options.top_p is not supported by current Claude models")
	}
	if dialect.MaxTemperature > 0 && options.Temperature != nil && *options.Temperature > dialect.MaxTemperature {
		return nil, fmt.Errorf("anthropic: options.temperature must be between 0 and %g", dialect.MaxTemperature)
	}
	params.Model = options.Model
	if options.MaxOutputTokens != nil {
		params.MaxTokens = *options.MaxOutputTokens
	} else {
		params.MaxTokens = protocolDefaultMaxTokens
	}
	if options.Temperature != nil {
		params.Temperature = param.NewOpt(*options.Temperature)
	}
	if options.TopK != nil {
		params.TopK = param.NewOpt(*options.TopK)
	}
	if options.TopP != nil {
		params.TopP = param.NewOpt(*options.TopP)
	}
	if len(options.Stop) > 0 {
		params.StopSequences = slices.Clone(options.Stop)
	}
	if req.ToolChoice != nil {
		disableParallel := param.Opt[bool]{}
		switch req.ToolChoice.Parallelism {
		case corechat.ToolParallelismAllow:
			disableParallel = param.NewOpt(false)
		case corechat.ToolParallelismSingle:
			disableParallel = param.NewOpt(true)
		}
		switch req.ToolChoice.Mode {
		case corechat.ToolChoiceAuto:
			params.ToolChoice.OfAuto = &anthropicsdk.ToolChoiceAutoParam{DisableParallelToolUse: disableParallel}
		case corechat.ToolChoiceNone:
			none := anthropicsdk.NewToolChoiceNoneParam()
			params.ToolChoice.OfNone = &none
		case corechat.ToolChoiceRequired:
			params.ToolChoice.OfAny = &anthropicsdk.ToolChoiceAnyParam{DisableParallelToolUse: disableParallel}
		case corechat.ToolChoiceNamed:
			params.ToolChoice = anthropicsdk.ToolChoiceParamOfTool(req.ToolChoice.Name)
			params.ToolChoice.OfTool.DisableParallelToolUse = disableParallel
		}
	}
	reasoningEffort, err := mapProtocolReasoningEffort(options.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	params.OutputConfig.Effort = reasoningEffort

	system, messages, err := mapProtocolMessages(req.Messages, dialect.Provider)
	if err != nil {
		return nil, err
	}
	params.System = append(params.System, system...)
	params.Messages = append(params.Messages, messages...)
	if mapProtocolOutputFormatErr := mapProtocolOutputFormat(options.OutputFormat, dialect, &params); mapProtocolOutputFormatErr != nil {
		return nil, mapProtocolOutputFormatErr
	}
	tools, err := mapProtocolTools(req.Tools)
	if err != nil {
		return nil, err
	}
	params.Tools = append(params.Tools, tools...)
	return &params, nil
}

func projectMessageInputTokenCount(params *anthropicsdk.MessageNewParams) (*anthropicsdk.MessageCountTokensParams, error) {
	encoded, err := jsonv2.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode input token count request: %w", err)
	}
	var projected anthropicsdk.MessageCountTokensParams
	if err := jsonv2.Unmarshal(encoded, &projected); err != nil {
		return nil, fmt.Errorf("anthropic: project input token count request: %w", err)
	}
	projected.Messages = params.Messages
	projected.Model = params.Model
	projected.System.OfTextBlockArray = params.System
	return &projected, nil
}

func decodeOutputConfig(fields map[string]any, extensionKey string) (anthropicsdk.OutputConfigParam, error) {
	value, exists := fields["output_config"]
	if !exists {
		return anthropicsdk.OutputConfigParam{}, nil
	}
	var extraFields map[string]any
	if object, ok := value.(map[string]any); ok {
		if _, exists := object["format"]; exists {
			return anthropicsdk.OutputConfigParam{}, fmt.Errorf("anthropic: extension %q field %q.format is owned by options.output_format", extensionKey, "output_config")
		}
		if _, exists := object["effort"]; exists {
			return anthropicsdk.OutputConfigParam{}, fmt.Errorf("anthropic: extension %q field %q.effort is owned by options.reasoning_effort", extensionKey, "output_config")
		}
		extraFields = maps.Clone(object)
	}
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return anthropicsdk.OutputConfigParam{}, fmt.Errorf("anthropic: extension %q field %q: %w", extensionKey, "output_config", err)
	}
	var config anthropicsdk.OutputConfigParam
	if err := jsonv2.Unmarshal(encoded, &config); err != nil {
		return anthropicsdk.OutputConfigParam{}, fmt.Errorf("anthropic: extension %q field %q: %w", extensionKey, "output_config", err)
	}
	if len(extraFields) > 0 {
		config.SetExtraFields(extraFields)
	}
	delete(fields, "output_config")
	return config, nil
}

func mapProtocolReasoningEffort(effort corechat.ReasoningEffort) (anthropicsdk.OutputConfigEffort, error) {
	switch effort {
	case "":
		return "", nil
	case corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortLow):
		return anthropicsdk.OutputConfigEffortLow, nil
	case corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortMedium):
		return anthropicsdk.OutputConfigEffortMedium, nil
	case corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortHigh):
		return anthropicsdk.OutputConfigEffortHigh, nil
	case corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortXhigh):
		return anthropicsdk.OutputConfigEffortXhigh, nil
	case corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortMax):
		return anthropicsdk.OutputConfigEffortMax, nil
	default:
		return "", fmt.Errorf("anthropic: options.reasoning_effort has unsupported value %q", effort)
	}
}

func mapProtocolOutputFormat(format *corechat.OutputFormat, dialect Dialect, params *anthropicsdk.MessageNewParams) error {
	if format == nil || format.Type == corechat.OutputFormatText {
		return nil
	}
	if !dialect.NativeJSONSchema {
		return fmt.Errorf("%w: anthropic-compatible endpoint does not support %q", corechat.ErrUnsupportedOutputFormat, format.Type)
	}
	var schema map[string]any
	if format.Type == corechat.OutputFormatJSON {
		schema = map[string]any{"type": "object"}
	} else {
		var err error
		schema, err = format.SchemaAs[map[string]any]()
		if err != nil {
			return fmt.Errorf("anthropic: output schema: %w", err)
		}
	}
	params.OutputConfig.Format = anthropicsdk.JSONOutputFormatParam{Schema: schema}
	return nil
}

func mapProtocolMessages(messages []corechat.Message, provider string) ([]anthropicsdk.TextBlockParam, []anthropicsdk.MessageParam, error) {
	var system []anthropicsdk.TextBlockParam
	conversation := make([]anthropicsdk.MessageParam, 0, len(messages))
	for i := range messages {
		message := messages[i]
		switch message.Role {
		case corechat.RoleSystem:
			for j := range message.Parts {
				system = append(system, anthropicsdk.TextBlockParam{Text: message.Parts[j].Text})
			}
		case corechat.RoleUser:
			blocks, err := mapProtocolUserParts(message.Parts)
			if err != nil {
				return nil, nil, fmt.Errorf("anthropic: messages[%d]: %w", i, err)
			}
			conversation = append(conversation, anthropicsdk.NewUserMessage(blocks...))
		case corechat.RoleAssistant:
			blocks, err := mapProtocolAssistant(message, provider)
			if err != nil {
				return nil, nil, fmt.Errorf("anthropic: messages[%d]: %w", i, err)
			}
			conversation = append(conversation, anthropicsdk.NewAssistantMessage(blocks...))
		case corechat.RoleTool:
			blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(message.Parts))
			for j := range message.Parts {
				result := message.Parts[j].ToolResult
				block, err := mapProtocolToolResult(*result)
				if err != nil {
					return nil, nil, fmt.Errorf("anthropic: messages[%d].parts[%d]: %w", i, j, err)
				}
				blocks = append(blocks, block)
			}
			conversation = append(conversation, anthropicsdk.NewUserMessage(blocks...))
		default:
			return nil, nil, fmt.Errorf("anthropic: messages[%d]: unsupported role %q", i, message.Role)
		}
	}
	return system, conversation, nil
}

func mapProtocolToolResult(result corechat.ToolResult) (anthropicsdk.ContentBlockParamUnion, error) {
	content := make([]anthropicsdk.ToolResultBlockParamContentUnion, 0, max(1, len(result.Output.Content)))
	if len(result.Output.Content) == 0 {
		content = append(content, anthropicsdk.ToolResultBlockParamContentUnion{
			OfText: &anthropicsdk.TextBlockParam{Text: string(result.Output.Details)},
		})
	} else {
		for index := range result.Output.Content {
			part := result.Output.Content[index]
			switch part.Kind {
			case corechat.PartText:
				content = append(content, anthropicsdk.ToolResultBlockParamContentUnion{
					OfText: &anthropicsdk.TextBlockParam{Text: part.Text},
				})
			case corechat.PartMedia:
				mapped, err := mapProtocolMedia(part.Media)
				if err != nil {
					return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("tool output content[%d]: %w", index, err)
				}
				switch {
				case mapped.OfImage != nil:
					content = append(content, anthropicsdk.ToolResultBlockParamContentUnion{OfImage: mapped.OfImage})
				case mapped.OfDocument != nil:
					content = append(content, anthropicsdk.ToolResultBlockParamContentUnion{OfDocument: mapped.OfDocument})
				default:
					return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("tool output content[%d]: unsupported mapped media", index)
				}
			default:
				return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("tool output content[%d]: unsupported part %q", index, part.Kind)
			}
		}
	}
	return anthropicsdk.ContentBlockParamUnion{OfToolResult: &anthropicsdk.ToolResultBlockParam{
		ToolUseID: result.ID,
		IsError:   anthropicsdk.Bool(result.IsError),
		Content:   content,
	}}, nil
}

func mapProtocolUserParts(parts []corechat.Part) ([]anthropicsdk.ContentBlockParamUnion, error) {
	blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(parts))
	for i := range parts {
		switch parts[i].Kind {
		case corechat.PartText:
			blocks = append(blocks, anthropicsdk.NewTextBlock(parts[i].Text))
		case corechat.PartMedia:
			block, err := mapProtocolMedia(parts[i].Media)
			if err != nil {
				return nil, fmt.Errorf("parts[%d]: %w", i, err)
			}
			blocks = append(blocks, block)
		default:
			return nil, fmt.Errorf("parts[%d]: unsupported user part %q", i, parts[i].Kind)
		}
	}
	return blocks, nil
}

func mapProtocolMedia(value *media.Media) (anthropicsdk.ContentBlockParamUnion, error) {
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil {
		return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("media MIME %q: %w", value.MIME, err)
	}
	if strings.HasPrefix(mediaType, "image/") {
		switch value.Source.Kind {
		case media.SourceBytes:
			if !protocolImageMIME(mediaType) {
				return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("image MIME %q is unsupported", mediaType)
			}
			data, bytesErr := value.Bytes()
			if bytesErr != nil {
				return anthropicsdk.ContentBlockParamUnion{}, bytesErr
			}
			return anthropicsdk.NewImageBlockBase64(mediaType, base64.StdEncoding.EncodeToString(data)), nil
		case media.SourceURI:
			uri, uriErr := value.URI()
			if uriErr != nil {
				return anthropicsdk.ContentBlockParamUnion{}, uriErr
			}
			return anthropicsdk.NewImageBlock(anthropicsdk.URLImageSourceParam{URL: uri}), nil
		default:
			return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("image source %q is unsupported", value.Source.Kind)
		}
	}
	if mediaType == "application/pdf" {
		var block anthropicsdk.ContentBlockParamUnion
		switch value.Source.Kind {
		case media.SourceBytes:
			data, bytesErr := value.Bytes()
			if bytesErr != nil {
				return anthropicsdk.ContentBlockParamUnion{}, bytesErr
			}
			block = anthropicsdk.NewDocumentBlock(anthropicsdk.Base64PDFSourceParam{Data: base64.StdEncoding.EncodeToString(data)})
		case media.SourceURI:
			uri, uriErr := value.URI()
			if uriErr != nil {
				return anthropicsdk.ContentBlockParamUnion{}, uriErr
			}
			block = anthropicsdk.NewDocumentBlock(anthropicsdk.URLPDFSourceParam{URL: uri})
		default:
			return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("PDF source %q is unsupported", value.Source.Kind)
		}
		if value.Name != "" {
			block.OfDocument.Title = param.NewOpt(value.Name)
		}
		return block, nil
	}
	return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("media MIME %q is unsupported", mediaType)
}

func protocolImageMIME(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func mapProtocolAssistant(message corechat.Message, provider string) ([]anthropicsdk.ContentBlockParamUnion, error) {
	blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(message.Parts))
	for i := range message.Parts {
		block, include, err := mapProtocolAssistantPart(i, message.Parts[i], provider)
		if err != nil {
			return nil, err
		}
		if include {
			blocks = append(blocks, block)
		}
	}
	return blocks, nil
}

func mapProtocolAssistantPart(index int, part corechat.Part, provider string) (anthropicsdk.ContentBlockParamUnion, bool, error) {
	switch part.Kind {
	case corechat.PartText:
		return anthropicsdk.NewTextBlock(part.Text), true, nil
	case corechat.PartReasoning:
		return mapProtocolReasoningPart(index, part, provider)
	case corechat.PartToolCall:
		block, err := mapProtocolToolCall(index, *part.ToolCall)
		return block, true, err
	case corechat.PartRefusal:
		return anthropicsdk.NewTextBlock(part.Text), true, nil
	default:
		return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d]: unsupported assistant part %q", index, part.Kind)
	}
}

func mapProtocolReasoningPart(index int, part corechat.Part, provider string) (anthropicsdk.ContentBlockParamUnion, bool, error) {
	issuer, found, err := part.Metadata.Decode[string](protocolReasoningProviderKey)
	if err != nil {
		return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d].metadata[%q]: %w", index, protocolReasoningProviderKey, err)
	}
	if !found || issuer != provider {
		return anthropicsdk.ContentBlockParamUnion{}, false, nil
	}
	kind, found, err := part.Metadata.Decode[string](protocolReasoningKindKey)
	if err != nil {
		return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d].metadata[%q]: %w", index, protocolReasoningKindKey, err)
	}
	if !found {
		return anthropicsdk.ContentBlockParamUnion{}, false, nil
	}
	switch kind {
	case protocolReasoningThinking:
		if len(part.ReasoningState) == 0 {
			return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d]: Anthropic thinking requires its provider signature", index)
		}
		return anthropicsdk.NewThinkingBlock(string(part.ReasoningState), part.Text), true, nil
	case protocolReasoningRedacted:
		if len(part.ReasoningState) == 0 {
			return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d]: Anthropic redacted thinking requires opaque data", index)
		}
		return anthropicsdk.NewRedactedThinkingBlock(string(part.ReasoningState)), true, nil
	default:
		return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d].metadata[%q]: unknown kind %q", index, protocolReasoningKindKey, kind)
	}
}

func mapProtocolToolCall(index int, call corechat.ToolCall) (anthropicsdk.ContentBlockParamUnion, error) {
	var input any
	if call.Arguments == "" {
		input = map[string]any{}
	} else if err := jsonv2.Unmarshal([]byte(call.Arguments), &input); err != nil {
		return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("parts[%d].tool_call.arguments: %w", index, err)
	}
	return anthropicsdk.NewToolUseBlock(call.ID, input, call.Name), nil
}

func mapProtocolTools(definitions []corechat.ToolDefinition) ([]anthropicsdk.ToolUnionParam, error) {
	tools := make([]anthropicsdk.ToolUnionParam, 0, len(definitions))
	for i := range definitions {
		var schema map[string]any
		if err := jsonv2.Unmarshal(definitions[i].InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("anthropic: tools[%d].input_schema: %w", i, err)
		}
		delete(schema, "type")
		tool := anthropicsdk.ToolParam{
			Name:        definitions[i].Name,
			Description: param.NewOpt(definitions[i].Description),
			InputSchema: anthropicsdk.ToolInputSchemaParam{ExtraFields: schema},
		}
		tools = append(tools, anthropicsdk.ToolUnionParam{OfTool: &tool})
	}
	return tools, nil
}
