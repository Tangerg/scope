package mistral

import "encoding/json"

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
	Usage   chatUsage              `json:"usage"`
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
	Usage   chatUsage                    `json:"usage"`
}

type chatCompletionStreamChoice struct {
	Index        int                   `json:"index"`
	Delta        chatCompletionMessage `json:"delta"`
	FinishReason finishReason          `json:"finish_reason"`
}

type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	NumCachedTokens     int64 `json:"num_cached_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}
