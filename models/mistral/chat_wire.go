package mistral

import (
	"encoding/json"
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
)

type chatRole string

const (
	chatRoleSystem    chatRole = "system"
	chatRoleUser      chatRole = "user"
	chatRoleAssistant chatRole = "assistant"
	chatRoleTool      chatRole = "tool"
)

type contentType string

const (
	contentTypeText          contentType = "text"
	contentTypeThinking      contentType = "thinking"
	contentTypeImageURL      contentType = "image_url"
	contentTypeDocumentURL   contentType = "document_url"
	contentTypeInputAudio    contentType = "input_audio"
	contentTypeReference     contentType = "reference"
	contentTypeToolReference contentType = "tool_reference"
)

type toolType string

const toolTypeFunction toolType = "function"

type toolChoice string

const (
	toolChoiceAuto toolChoice = "auto"
	toolChoiceNone toolChoice = "none"
	toolChoiceAny  toolChoice = "any"
)

type outputFormatType string

const (
	outputFormatTypeText       outputFormatType = "text"
	outputFormatTypeJSONObject outputFormatType = "json_object"
	outputFormatTypeJSONSchema outputFormatType = "json_schema"
)

type finishReason string

const (
	finishReasonStop        finishReason = "stop"
	finishReasonLength      finishReason = "length"
	finishReasonModelLength finishReason = "model_length"
	finishReasonToolCalls   finishReason = "tool_calls"
	// finishReasonError is the fifth value Mistral's own client declares
	// alongside the four above. It has no portable Core match, and reaching it
	// through a default branch would have filed it under "not classified" —
	// which is the one thing [corechat.FinishReasonOther] is documented not to
	// mean.
	finishReasonError finishReason = "error"
)

// normalized maps the five values Mistral's client declares —
// stop, length, model_length, error and tool_calls — and answers anything else
// with Other, because that client types the field as those literals or an
// unrecognized string, so a value outside the set is something this adapter has
// not seen rather than something it failed to classify.
//
// error and an unrecognized value both land on Other, which is what Core
// reserves for a known terminal state with no portable match. Neither is
// silently lost: finishReason.metadata keeps the provider's own word for
// it on the output, the way this adapter's siblings do, so a caller can tell an
// errored generation from a provider iteration limit.
func (f finishReason) normalized() corechat.FinishReason {
	switch f {
	case "":
		return ""
	case finishReasonStop:
		return corechat.FinishReasonStop
	case finishReasonLength, finishReasonModelLength:
		return corechat.FinishReasonLength
	case finishReasonToolCalls:
		return corechat.FinishReasonToolCalls
	case finishReasonError:
		return corechat.FinishReasonOther
	default:
		return corechat.FinishReasonOther
	}
}

// metadata records the provider's own finish reason
// whenever the portable one is Other, so the distinction Other erases stays
// available.
func (f finishReason) metadata(

	mapped corechat.FinishReason,
) (*corechat.OutputMetadata, error) {
	if mapped != corechat.FinishReasonOther {
		return nil, nil
	}
	outputMetadata := &corechat.OutputMetadata{}
	if err := outputMetadata.Extra.Set(nativeFinishReasonKey, string(f)); err != nil {
		return nil, fmt.Errorf("mistral: record native finish reason: %w", err)
	}
	return outputMetadata, nil
}

type responseFormat struct {
	Type       outputFormatType      `json:"type"`
	JSONSchema *jsonSchemaDefinition `json:"json_schema,omitempty"`
}

type jsonSchemaDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	Strict      bool            `json:"strict"`
}

type chatCompletionRequest struct {
	Model             string          `json:"model"`
	Messages          []chatMessage   `json:"messages"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	MaxTokens         *int64          `json:"max_tokens,omitempty"`
	Stream            bool            `json:"stream"`
	Stop              []string        `json:"stop,omitempty"`
	PresencePenalty   *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64        `json:"frequency_penalty,omitempty"`
	Tools             []chatTool      `json:"tools,omitempty"`
	ToolChoice        toolChoice      `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    *responseFormat `json:"response_format,omitempty"`
	ChatRequestOptions
}

type chatMessage struct {
	Role       chatRole       `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type textChunk struct {
	Type contentType `json:"type"`
	Text string      `json:"text"`
}

type thinkChunk struct {
	Type     contentType `json:"type"`
	Thinking []textChunk `json:"thinking"`
	Closed   bool        `json:"closed"`
}

type imageURLChunk struct {
	Type     contentType   `json:"type"`
	ImageURL imageURLValue `json:"image_url"`
}

type imageURLValue string

func (i imageURLValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(i))
}

func (i *imageURLValue) UnmarshalJSON(data []byte) error {
	var direct string
	if err := json.Unmarshal(data, &direct); err == nil {
		*i = imageURLValue(direct)
		return nil
	}
	var object struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*i = imageURLValue(object.URL)
	return nil
}

type documentURLChunk struct {
	Type         contentType `json:"type"`
	DocumentURL  string      `json:"document_url"`
	DocumentName string      `json:"document_name,omitempty"`
}

type audioChunk struct {
	Type       contentType `json:"type"`
	InputAudio string      `json:"input_audio"`
}

type chatTool struct {
	Type     toolType     `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type chatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     toolType         `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
	Index    int              `json:"index,omitempty"`
}

type chatFunctionCall struct {
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   *chatUsage             `json:"usage"`
}

func (c *chatCompletionResponse) response() (*corechat.Response, error) {
	if c == nil {
		return nil, errors.New("mistral: nil chat completion response")
	}
	if len(c.Choices) != expectedResponseChoices {
		return nil, fmt.Errorf("mistral: response has %d choices; Core requires one output", len(c.Choices))
	}
	response := &corechat.Response{
		Metadata: &corechat.ResponseMetadata{
			ID: c.ID, Model: c.Model, Usage: c.Usage.usage(),
		},
	}
	if err := response.Metadata.Extra.Set(responseExtensionKey, c); err != nil {
		return nil, err
	}
	wireChoice := c.Choices[0]
	if wireChoice.Index != firstChoiceIndex {
		return nil, fmt.Errorf("mistral: choice index is %d, want %d", wireChoice.Index, firstChoiceIndex)
	}
	parts, err := mapMistralContent(wireChoice.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("mistral: output message content: %w", err)
	}
	toolParts, err := mapMistralToolCalls(wireChoice.Message.ToolCalls)
	if err != nil {
		return nil, fmt.Errorf("mistral: output message tool calls: %w", err)
	}
	parts = append(parts, toolParts...)
	finish := wireChoice.FinishReason.normalized()
	nativeFinish, err := wireChoice.FinishReason.metadata(finish)
	if err != nil {
		return nil, err
	}
	response.Output = &corechat.Output{FinishReason: finish, Metadata: nativeFinish}
	if len(parts) > 0 {
		response.Output.Message = &corechat.Message{Role: corechat.RoleAssistant, Parts: parts}
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("mistral: mapped chat completion: %w", err)
	}
	return response, nil
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      chatCompletionMessage `json:"message"`
	FinishReason finishReason          `json:"finish_reason"`
}

type chatCompletionMessage struct {
	Role      chatRole        `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []chatToolCall  `json:"tool_calls"`
}

type chatCompletionChunk struct {
	ID      string                       `json:"id"`
	Model   string                       `json:"model"`
	Choices []chatCompletionStreamChoice `json:"choices"`
	Usage   *chatUsage                   `json:"usage"`
}

type chatCompletionStreamChoice struct {
	Index        int                   `json:"index"`
	Delta        chatCompletionMessage `json:"delta"`
	FinishReason finishReason          `json:"finish_reason"`
}

type chatUsage struct {
	PromptTokens        *int64 `json:"prompt_tokens"`
	CompletionTokens    *int64 `json:"completion_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	NumCachedTokens     int64  `json:"num_cached_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (c *chatUsage) usage() *corechat.Usage {
	if c == nil || c.PromptTokens == nil || c.CompletionTokens == nil {
		return nil
	}
	mapped := corechat.Usage{InputTokens: *c.PromptTokens, OutputTokens: *c.CompletionTokens}
	cached := c.NumCachedTokens
	if c.PromptTokensDetails != nil && c.PromptTokensDetails.CachedTokens != 0 {
		cached = c.PromptTokensDetails.CachedTokens
	}
	if cached != 0 {
		mapped.CacheReadInputTokens = &cached
	}
	return &mapped
}
