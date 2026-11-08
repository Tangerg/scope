package anthropic

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"mime"
	"slices"
	"strings"

	corecontent "github.com/Tangerg/scope/core/content"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

const (
	requestFieldModel         = "model"
	requestFieldMessages      = "messages"
	requestFieldSystem        = "system"
	requestFieldTools         = "tools"
	requestFieldStream        = "stream"
	requestFieldMaxTokens     = "max_tokens"
	requestFieldTemperature   = "temperature"
	requestFieldToolChoice    = "tool_choice"
	requestFieldTopK          = "top_k"
	requestFieldTopP          = "top_p"
	requestFieldStopSequences = "stop_sequences"
	requestFieldOutputConfig  = "output_config"
	outputConfigFieldFormat   = "format"
	outputConfigFieldEffort   = "effort"
	mediaTypeImageJPEG        = "image/jpeg"
	mediaTypeImagePNG         = "image/png"
	mediaTypeImageGIF         = "image/gif"
	mediaTypeImageWebP        = "image/webp"
)

const (
	// RequestExtensionKey stores provider-owned top-level Messages API fields in
	// a Core request for fields without a provider-neutral equivalent.
	RequestExtensionKey          = ProviderID + "/request"
	protocolReasoningKindKey     = "anthropic/reasoning_kind"
	protocolReasoningProviderKey = "anthropic/reasoning_provider"
	protocolContentBlockIndexKey = "anthropic/content_block_index"
	protocolNativeStopReasonKey  = "anthropic/native_stop_reason"
	protocolStopSequenceKey      = "anthropic/stop_sequence"
	protocolUsageKey             = "anthropic/usage"
	inlineDocumentDigestPrefix   = "urn:sha256:"
)

type protocolRequest struct {
	params    *anthropicsdk.MessageNewParams
	documents protocolDocumentSources
}

func mapProtocolRequest(defaults corechat.Options, req *corechat.Request, dialect Dialect) (*protocolRequest, error) {
	options, err := defaults.Resolve(req.Options)
	if err != nil {
		return nil, fmt.Errorf("anthropic: options: %w", err)
	}
	req = req.Clone()
	req.Options = options
	extensionKey := protocolRequestExtensionKey(dialect.Provider)
	encodedFields, _, err := req.Options.Extensions.Decode[metadata.Map](extensionKey)
	if err != nil {
		return nil, fmt.Errorf("anthropic: extension %q: %w", extensionKey, err)
	}
	fields, err := encodedFields.Values()
	if err != nil {
		return nil, err
	}
	if dialect.PrepareRequest != nil {
		if fields == nil {
			fields = make(map[string]any)
		}
		if prepareErr := dialect.PrepareRequest(req, fields); prepareErr != nil {
			return nil, fmt.Errorf("anthropic: prepare %s request: %w", dialect.Provider, prepareErr)
		}
		options = req.Options
	}
	for _, name := range []string{requestFieldModel, requestFieldMessages, requestFieldSystem, requestFieldTools, requestFieldStream, requestFieldMaxTokens, requestFieldTemperature, requestFieldToolChoice, requestFieldTopK, requestFieldTopP, requestFieldStopSequences} {
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("anthropic: extension %q field %q is owned by Core", extensionKey, name)
		}
	}
	// Revision and sampling support are independent endpoint capabilities;
	// metadata headers cannot enable request fields.
	for _, sampling := range []struct {
		name  string
		value any
		set   bool
	}{
		{requestFieldTemperature, options.Temperature, options.Temperature != nil},
		{requestFieldTopK, options.TopK, options.TopK != nil},
		{requestFieldTopP, options.TopP, options.TopP != nil},
	} {
		if !sampling.set {
			continue
		}
		if !dialect.SupportsSampling {
			return nil, fmt.Errorf("anthropic: options.%s is not supported by the native Messages API", sampling.name)
		}
		if fields == nil {
			fields = make(map[string]any)
		}
		fields[sampling.name] = sampling.value
	}
	outputConfig, err := decodeOutputConfig(fields, extensionKey)
	if err != nil {
		return nil, err
	}
	params := anthropicsdk.MessageNewParams{OutputConfig: outputConfig}
	nativeFields, err := nativeJSONFields(fields)
	if err != nil {
		return nil, err
	}
	params.SetExtraFields(nativeFields)

	if options.Model == "" {
		return nil, errors.New("anthropic: model is required in defaults or request options")
	}
	if options.FrequencyPenalty != nil {
		return nil, errors.New("anthropic: options.frequency_penalty is not supported")
	}
	if options.PresencePenalty != nil {
		return nil, errors.New("anthropic: options.presence_penalty is not supported")
	}
	params.Model = options.Model
	if options.MaxOutputTokens != nil {
		params.MaxTokens = *options.MaxOutputTokens
	} else {
		params.MaxTokens = protocolDefaultMaxTokens
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

	mapper := protocolMessageMapper{provider: dialect.Provider}
	system, messages, err := mapper.mapMessages(req.Messages)
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
	return &protocolRequest{params: &params, documents: mapper.documents}, nil
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
	projected.OutputConfig = params.OutputConfig
	if params.Tools != nil {
		projected.Tools = make([]anthropicsdk.MessageCountTokensToolUnionParam, len(params.Tools))
		for index, definition := range params.Tools {
			projected.Tools[index] = anthropicsdk.MessageCountTokensToolUnionParam(definition)
		}
	}
	return &projected, nil
}

func decodeOutputConfig(fields map[string]any, extensionKey string) (anthropicsdk.OutputConfigParam, error) {
	value, exists := fields[requestFieldOutputConfig]
	if !exists {
		return anthropicsdk.OutputConfigParam{}, nil
	}
	var extraFields map[string]any
	if object, ok := value.(map[string]any); ok {
		if _, exists := object[outputConfigFieldFormat]; exists {
			return anthropicsdk.OutputConfigParam{}, fmt.Errorf("anthropic: extension %q field %q.format is owned by options.output_format", extensionKey, requestFieldOutputConfig)
		}
		if _, exists := object[outputConfigFieldEffort]; exists {
			return anthropicsdk.OutputConfigParam{}, fmt.Errorf("anthropic: extension %q field %q.effort is owned by options.reasoning_effort", extensionKey, requestFieldOutputConfig)
		}
		extraFields = maps.Clone(object)
	}
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return anthropicsdk.OutputConfigParam{}, fmt.Errorf("anthropic: extension %q field %q: %w", extensionKey, requestFieldOutputConfig, err)
	}
	var config anthropicsdk.OutputConfigParam
	if err := jsonv2.Unmarshal(encoded, &config); err != nil {
		return anthropicsdk.OutputConfigParam{}, fmt.Errorf("anthropic: extension %q field %q: %w", extensionKey, requestFieldOutputConfig, err)
	}
	if len(extraFields) > 0 {
		nativeFields, err := nativeJSONFields(extraFields)
		if err != nil {
			return anthropicsdk.OutputConfigParam{}, err
		}
		config.SetExtraFields(nativeFields)
	}
	delete(fields, requestFieldOutputConfig)
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
	nativeSchema, err := nativeOutputSchema(format)
	if err != nil {
		return err
	}
	params.OutputConfig.Format = anthropicsdk.JSONOutputFormatParam{Schema: nativeSchema}
	return nil
}

// Document positions belong to the transmitted message sequence, including
// tool results; recording them during projection prevents a second traversal
// from assigning citations to different sources.
type protocolMessageMapper struct {
	provider  string
	documents protocolDocumentSources
}

func (p *protocolMessageMapper) mapMessages(messages []corechat.Message) ([]anthropicsdk.TextBlockParam, []anthropicsdk.MessageParam, error) {
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
			blocks, err := p.mapUserParts(message.Parts)
			if err != nil {
				return nil, nil, fmt.Errorf("anthropic: messages[%d]: %w", i, err)
			}
			conversation = append(conversation, anthropicsdk.NewUserMessage(blocks...))
		case corechat.RoleAssistant:
			blocks, err := mapProtocolAssistant(message, p.provider)
			if err != nil {
				return nil, nil, fmt.Errorf("anthropic: messages[%d]: %w", i, err)
			}
			conversation = append(conversation, anthropicsdk.NewAssistantMessage(blocks...))
		case corechat.RoleTool:
			blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(message.Parts))
			for j := range message.Parts {
				result := message.Parts[j].ToolResult
				block, err := p.mapToolResult(*result)
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

func (p *protocolMessageMapper) mapToolResult(result corechat.ToolResult) (anthropicsdk.ContentBlockParamUnion, error) {
	content := make([]anthropicsdk.ToolResultBlockParamContentUnion, 0, max(1, len(result.Output.Content)))
	if len(result.Output.Content) == 0 {
		content = append(content, anthropicsdk.ToolResultBlockParamContentUnion{
			OfText: &anthropicsdk.TextBlockParam{Text: string(result.Output.Details)},
		})
	} else {
		for index := range result.Output.Content {
			part := result.Output.Content[index]
			switch part.Kind {
			case corecontent.Text:
				content = append(content, anthropicsdk.ToolResultBlockParamContentUnion{
					OfText: &anthropicsdk.TextBlockParam{Text: part.Text},
				})
			case corecontent.Media:
				mapped, err := p.mapMedia(part.Media)
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

func (p *protocolMessageMapper) mapUserParts(parts []corechat.Part) ([]anthropicsdk.ContentBlockParamUnion, error) {
	blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(parts))
	for i := range parts {
		switch parts[i].Kind {
		case corechat.PartText:
			if len(parts[i].Citations) != 0 {
				return nil, fmt.Errorf("%w: user text cannot carry Anthropic citations", errors.ErrUnsupported)
			}
			blocks = append(blocks, anthropicsdk.NewTextBlock(parts[i].Text))
		case corechat.PartMedia:
			block, err := p.mapMedia(parts[i].Media)
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

func (p *protocolMessageMapper) mapMedia(value *media.Media) (anthropicsdk.ContentBlockParamUnion, error) {
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
		p.bindDocument(value)
		return block, nil
	}
	return anthropicsdk.ContentBlockParamUnion{}, fmt.Errorf("media MIME %q is unsupported", mediaType)
}

func (p *protocolMessageMapper) bindDocument(value *media.Media) {
	var source corecontent.CitationSource
	switch {
	case value.Source.Kind == media.SourceURI:
		source = corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: value.Source.URI}
	case value.ID != "":
		source = corecontent.CitationSource{Kind: corecontent.CitationSourceReference, Value: value.ID}
	default:
		// Anonymous inline documents have no external locator. Their bytes, not
		// a title or response ID, define a stable content-addressed reference.
		digest := sha256.Sum256(value.Source.Bytes)
		source = corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: fmt.Sprintf("%s%x", inlineDocumentDigestPrefix, digest)}
	}
	p.documents = append(p.documents, source)
}

func protocolImageMIME(mediaType string) bool {
	switch mediaType {
	case mediaTypeImageJPEG, mediaTypeImagePNG, mediaTypeImageGIF, mediaTypeImageWebP:
		return true
	default:
		return false
	}
}

func mapProtocolAssistant(message corechat.Message, provider string) ([]anthropicsdk.ContentBlockParamUnion, error) {
	if len(message.Parts) == 0 {
		return nil, errors.New("assistant metadata has no replayable Messages content")
	}
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
		block, err := mapProtocolToolCall(*part.ToolCall)
		return block, true, err
	case corechat.PartRefusal:
		return anthropicsdk.NewTextBlock(part.Text), true, nil
	default:
		return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d]: unsupported assistant part %q", index, part.Kind)
	}
}

func mapProtocolReasoningPart(index int, part corechat.Part, provider string) (anthropicsdk.ContentBlockParamUnion, bool, error) {
	kind, found, err := reasoningBlockKindFor(part, provider)
	if err != nil {
		return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d]: %w", index, err)
	}
	if !found {
		return anthropicsdk.ContentBlockParamUnion{}, false, nil
	}
	switch kind {
	case ReasoningBlockThinking:
		return anthropicsdk.NewThinkingBlock(string(part.ReasoningState), part.Text), true, nil
	case ReasoningBlockRedacted:
		return anthropicsdk.NewRedactedThinkingBlock(string(part.ReasoningState)), true, nil
	default:
		return anthropicsdk.ContentBlockParamUnion{}, false, fmt.Errorf("parts[%d].metadata[%q]: unknown kind %q", index, protocolReasoningKindKey, kind)
	}
}

func mapProtocolToolCall(call corechat.ToolCall) (anthropicsdk.ContentBlockParamUnion, error) {
	input := json.RawMessage(call.Arguments)
	if call.Arguments == "" {
		input = json.RawMessage(`{}`)
	}
	return anthropicsdk.NewToolUseBlock(call.ID, input, call.Name), nil
}

func mapProtocolTools(definitions []toolcontract.Definition) ([]anthropicsdk.ToolUnionParam, error) {
	tools := make([]anthropicsdk.ToolUnionParam, 0, len(definitions))
	for i := range definitions {
		var encodedSchema metadata.Map
		if err := jsonv2.Unmarshal(definitions[i].InputSchema, &encodedSchema); err != nil {
			return nil, fmt.Errorf("anthropic: tools[%d].input_schema: %w", i, err)
		}
		schema, err := encodedSchema.Values()
		if err != nil {
			return nil, err
		}
		delete(schema, "type")
		nativeSchema, err := nativeJSONFields(schema)
		if err != nil {
			return nil, err
		}
		tool := anthropicsdk.ToolParam{
			Name:        definitions[i].Name,
			Description: param.NewOpt(definitions[i].Description),
			InputSchema: anthropicsdk.ToolInputSchemaParam{ExtraFields: nativeSchema},
		}
		tools = append(tools, anthropicsdk.ToolUnionParam{OfTool: &tool})
	}
	return tools, nil
}

// The SDK's encoder treats json.Number as a string. Encoded members keep each
// provider-owned JSON value intact across that encoder's open-field boundary.
func nativeJSONFields(fields map[string]any) (map[string]any, error) {
	encoded, err := metadata.FromValues(fields)
	if err != nil {
		return nil, err
	}
	if encoded == nil {
		return nil, nil
	}
	native := make(map[string]any, len(encoded))
	for name, value := range encoded {
		native[name] = value
	}
	return native, nil
}
