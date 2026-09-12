package openai

import (
	"encoding/json"
	"fmt"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

func projectResponsesInputTokenCount(params *responses.ResponseNewParams) (*responses.InputTokenCountParams, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("openai responses: encode input token count request: %w", err)
	}
	var projected responses.InputTokenCountParams
	if err := json.Unmarshal(encoded, &projected); err != nil {
		return nil, fmt.Errorf("openai responses: project input token count request: %w", err)
	}
	projected.Input.OfString = params.Input.OfString
	projected.Input.OfResponseInputItemArray = params.Input.OfInputItemList
	projected.Text.Format = params.Text.Format
	projected.Text.Verbosity = string(params.Text.Verbosity)
	return &projected, nil
}

func rejectCoreOwnedResponsesExtension(extensions metadata.Extensions) error {
	fields, found, err := extensions.Decode[map[string]json.RawMessage](ResponsesRequestExtensionKey)
	if err != nil {
		return fmt.Errorf("openai responses: extension %q: %w", ResponsesRequestExtensionKey, err)
	}
	if !found {
		return nil
	}
	for _, field := range []string{"parallel_tool_calls", "tool_choice"} {
		if _, exists := fields[field]; exists {
			return fmt.Errorf("openai responses: extension %q field %q is owned by options.tool_choice", ResponsesRequestExtensionKey, field)
		}
	}
	if raw, exists := fields["reasoning"]; exists {
		var reasoningFields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &reasoningFields); err != nil {
			return fmt.Errorf("openai responses: extension %q field %q: %w", ResponsesRequestExtensionKey, "reasoning", err)
		}
		if _, exists := reasoningFields["effort"]; exists {
			return fmt.Errorf("openai responses: extension %q field %q.effort is owned by options.reasoning_effort", ResponsesRequestExtensionKey, "reasoning")
		}
	}
	if raw, exists := fields["text"]; exists {
		var textFields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &textFields); err != nil {
			return fmt.Errorf("openai responses: extension %q field %q: %w", ResponsesRequestExtensionKey, "text", err)
		}
		if _, exists := textFields["format"]; exists {
			return fmt.Errorf("openai responses: extension %q field %q.format is owned by options.output_format", ResponsesRequestExtensionKey, "text")
		}
	}
	return nil
}

func mapResponsesOutputFormat(format *corechat.OutputFormat) (responses.ResponseFormatTextConfigUnionParam, error) {
	switch format.Type {
	case corechat.OutputFormatText:
		return responses.ResponseFormatTextConfigUnionParam{
			OfText: &shared.ResponseFormatTextParam{},
		}, nil
	case corechat.OutputFormatJSON:
		return responses.ResponseFormatTextConfigUnionParam{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		}, nil
	case corechat.OutputFormatJSONSchema:
		schema, err := format.SchemaAs[map[string]any]()
		if err != nil {
			return responses.ResponseFormatTextConfigUnionParam{}, fmt.Errorf("openai responses: output schema: %w", err)
		}
		definition := responses.ResponseFormatTextJSONSchemaConfigParam{
			Name:   format.Name,
			Schema: schema,
			Strict: openaisdk.Bool(true),
		}
		if format.Description != "" {
			definition.Description = openaisdk.String(format.Description)
		}
		return responses.ResponseFormatTextConfigUnionParam{OfJSONSchema: &definition}, nil
	default:
		return responses.ResponseFormatTextConfigUnionParam{}, fmt.Errorf("openai responses: unsupported output format %q", format.Type)
	}
}

func mapResponsesTools(definitions []corechat.ToolDefinition) ([]responses.ToolUnionParam, error) {
	tools := make([]responses.ToolUnionParam, 0, len(definitions))
	for index := range definitions {
		var schema map[string]any
		if err := json.Unmarshal(definitions[index].InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("openai responses: tools[%d].input_schema: %w", index, err)
		}
		tools = append(tools, responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
			Name: definitions[index].Name, Description: openaisdk.String(definitions[index].Description), Parameters: schema, Strict: openaisdk.Bool(true),
		}})
	}
	return tools, nil
}

func mapResponsesInput(messages []corechat.Message) (responses.ResponseInputParam, error) {
	items := make(responses.ResponseInputParam, 0, len(messages))
	for messageIndex := range messages {
		mapped, err := mapResponsesMessage(messageIndex, messages[messageIndex])
		if err != nil {
			return nil, fmt.Errorf("openai responses: messages[%d]: %w", messageIndex, err)
		}
		items = append(items, mapped...)
	}
	return items, nil
}

func mapResponsesMessage(messageIndex int, message corechat.Message) ([]responses.ResponseInputItemUnionParam, error) {
	switch message.Role {
	case corechat.RoleSystem:
		return []responses.ResponseInputItemUnionParam{responsesEasyMessage(responses.EasyInputMessageRoleSystem, message.Text())}, nil
	case corechat.RoleUser:
		content, err := mapResponsesUserContent(message.Parts)
		if err != nil {
			return nil, err
		}
		return []responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{
			Role:    responses.EasyInputMessageRoleUser,
			Content: responses.EasyInputMessageContentUnionParam{OfInputItemContentList: content},
		}}}, nil
	case corechat.RoleAssistant:
		return mapResponsesAssistantItems(message.Parts)
	case corechat.RoleTool:
		return mapResponsesToolResults(message.Parts)
	default:
		return nil, fmt.Errorf("unsupported role %q", message.Role)
	}
}

func responsesEasyMessage(role responses.EasyInputMessageRole, text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{
		Role: role, Content: responses.EasyInputMessageContentUnionParam{OfString: openaisdk.String(text)},
	}}
}

func mapResponsesUserContent(parts []corechat.Part) (responses.ResponseInputMessageContentListParam, error) {
	content := make(responses.ResponseInputMessageContentListParam, 0, len(parts))
	for index := range parts {
		part := parts[index]
		switch part.Kind {
		case corechat.PartText:
			content = append(content, responses.ResponseInputContentUnionParam{OfInputText: &responses.ResponseInputTextParam{Text: part.Text}})
		case corechat.PartMedia:
			mapped, err := mapResponsesMedia(part.Media)
			if err != nil {
				return nil, fmt.Errorf("parts[%d]: %w", index, err)
			}
			content = append(content, mapped)
		}
	}
	return content, nil
}

func mapResponsesAssistantItems(parts []corechat.Part) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(parts))
	for partIndex := range parts {
		part := parts[partIndex]
		switch part.Kind {
		case corechat.PartText:
			items = append(items, responsesEasyMessage(responses.EasyInputMessageRoleAssistant, part.Text))
		case corechat.PartRefusal:
			items = append(items, responsesEasyMessage(responses.EasyInputMessageRoleAssistant, part.Text))
		case corechat.PartReasoning:
			if len(part.ReasoningState) == 0 {
				continue
			}
			reasoningItems, framed, err := decodeResponsesReasoningFrames(part.ReasoningState)
			if err != nil {
				return nil, fmt.Errorf("parts[%d].reasoning signature: %w", partIndex, err)
			}
			if !framed {
				// A signature from another provider is not valid OpenAI Responses
				// replay state. The visible text remains portable but is not sent as
				// a reasoning item without the provider-issued identity.
				continue
			}
			for index := range reasoningItems {
				item := reasoningItems[index]
				items = append(items, responses.ResponseInputItemUnionParam{OfReasoning: &item})
			}
		case corechat.PartToolCall:
			items = append(items, responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{
				CallID: part.ToolCall.ID, Name: part.ToolCall.Name, Arguments: part.ToolCall.Arguments,
			}})
		}
	}
	return items, nil
}

func mapResponsesToolResults(parts []corechat.Part) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(parts))
	for index := range parts {
		result := parts[index].ToolResult
		output, err := mapResponsesToolOutput(result.Output)
		if err != nil {
			return nil, fmt.Errorf("parts[%d].tool_result.output: %w", index, err)
		}
		items = append(items, responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: openaisdk.String(result.ID),
			Output: output,
		}})
	}
	return items, nil
}

func mapResponsesToolOutput(output corechat.ToolOutput) (responses.ResponseInputItemFunctionCallOutputOutputUnionParam, error) {
	if len(output.Content) == 0 {
		return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openaisdk.String(string(output.Details))}, nil
	}
	content := make(responses.ResponseFunctionCallOutputItemListParam, 0, len(output.Content))
	for index := range output.Content {
		part := output.Content[index]
		switch part.Kind {
		case corechat.PartText:
			content = append(content, responses.ResponseFunctionCallOutputItemParamOfInputText(part.Text))
		case corechat.PartMedia:
			mapped, err := mapResponsesToolMedia(part.Media)
			if err != nil {
				return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{}, fmt.Errorf("content[%d]: %w", index, err)
			}
			content = append(content, mapped)
		default:
			return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{}, fmt.Errorf("content[%d]: unsupported part %q", index, part.Kind)
		}
	}
	return responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfResponseFunctionCallOutputItemArray: content}, nil
}
