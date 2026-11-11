package mistral

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
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

type toolChoiceMode string

const (
	toolChoiceAuto toolChoiceMode = "auto"
	toolChoiceNone toolChoiceMode = "none"
	toolChoiceAny  toolChoiceMode = "any"
)

type toolChoice struct {
	mode toolChoiceMode
	name string
}

func (t toolChoice) MarshalJSON() ([]byte, error) {
	if t.name == "" {
		return jsonv2.Marshal(t.mode)
	}
	var named struct {
		Type     toolType `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	named.Type = toolTypeFunction
	named.Function.Name = t.name
	return jsonv2.Marshal(named)
}

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
	finishReasonError       finishReason = "error"
)

// normalized names error explicitly, though it lands on Other like an
// unrecognized value: it is a value Mistral's client declares, not one this
// adapter has failed to classify. preserveNative keeps both distinguishable.
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

// preserveNative records the provider's own finish reason whenever the
// portable one is Other, so the distinction Other erases stays available.
func (f finishReason) preserveNative(metadata *corechat.ResponseMetadata) error {
	if f.normalized() != corechat.FinishReasonOther {
		return nil
	}
	if err := metadata.Extra.Set(nativeFinishReasonKey, string(f)); err != nil {
		return fmt.Errorf("mistral: record native finish reason: %w", err)
	}
	return nil
}

type responseFormat struct {
	Type       outputFormatType      `json:"type"`
	JSONSchema *jsonSchemaDefinition `json:"json_schema,omitzero"`
}

type jsonSchemaDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	Strict      bool            `json:"strict"`
}

// JSON v2 cannot inline a field with custom codecs, so the wire projection
// carries the extension's fields without its public validation methods.
type chatRequestOptions ChatRequestOptions

type chatCompletionRequest struct {
	ReasoningEffort   corechat.ReasoningEffort `json:"reasoning_effort,omitempty"`
	Model             string                   `json:"model"`
	Messages          []chatMessage            `json:"messages"`
	Temperature       *float64                 `json:"temperature,omitzero"`
	TopP              *float64                 `json:"top_p,omitzero"`
	TopK              *int64                   `json:"top_k,omitzero"`
	MaxTokens         *int64                   `json:"max_tokens,omitzero"`
	Stream            bool                     `json:"stream"`
	Stop              []string                 `json:"stop,omitzero"`
	PresencePenalty   *float64                 `json:"presence_penalty,omitzero"`
	FrequencyPenalty  *float64                 `json:"frequency_penalty,omitzero"`
	Tools             []chatTool               `json:"tools,omitempty"`
	ToolChoice        *toolChoice              `json:"tool_choice,omitzero"`
	ParallelToolCalls *bool                    `json:"parallel_tool_calls,omitzero"`
	ResponseFormat    *responseFormat          `json:"response_format,omitzero"`
	chatRequestOptions
}

type chatMessage struct {
	Role       chatRole       `json:"role"`
	Content    any            `json:"content,omitzero"`
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
	return jsonv2.Marshal(string(i))
}

func (i *imageURLValue) UnmarshalJSON(data []byte) error {
	var direct string
	if err := jsonv2.Unmarshal(data, &direct); err == nil {
		*i = imageURLValue(direct)
		return nil
	}
	var object struct {
		URL string `json:"url"`
	}
	if err := jsonv2.Unmarshal(data, &object); err != nil {
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
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     toolType         `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
	Index    int              `json:"index,omitzero"`
}

func (c chatToolCall) validate() error {
	// Omission carries Mistral's function default; an unknown kind cannot be
	// promoted into an executable Core tool call merely because it has a name.
	if c.Type != "" && c.Type != toolTypeFunction {
		return fmt.Errorf("%w: tool call %d has unsupported type %q", corechat.ErrInvalidResponse, c.Index, c.Type)
	}
	return nil
}

type chatFunctionCall struct {
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitzero"`
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

func (c *chatCompletionStreamChoice) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("%w: stream choice receiver is nil", corechat.ErrInvalidResponse)
	}
	var decoded struct {
		Index        *int                  `json:"index"`
		Delta        chatCompletionMessage `json:"delta"`
		FinishReason finishReason          `json:"finish_reason"`
	}
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode stream choice: %w", corechat.ErrInvalidResponse, err)
	}
	if decoded.Index == nil {
		return fmt.Errorf("%w: stream choice requires index", corechat.ErrInvalidResponse)
	}
	*c = chatCompletionStreamChoice{Index: *decoded.Index, Delta: decoded.Delta, FinishReason: decoded.FinishReason}
	return nil
}

type chatUsage struct {
	PromptTokens        *int64 `json:"prompt_tokens"`
	CompletionTokens    *int64 `json:"completion_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (c *chatUsage) usage() *corechat.Usage {
	if c == nil || c.PromptTokens == nil || c.CompletionTokens == nil {
		return nil
	}
	mapped := corechat.Usage{InputTokens: *c.PromptTokens, OutputTokens: *c.CompletionTokens}
	if c.PromptTokensDetails != nil && c.PromptTokensDetails.CachedTokens != nil {
		mapped.CacheReadInputTokens = new(*c.PromptTokensDetails.CachedTokens)
	}
	return &mapped
}
