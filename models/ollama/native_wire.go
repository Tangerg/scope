package ollama

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	corechat "github.com/Tangerg/scope/core/chat"
	corevector "github.com/Tangerg/scope/core/vector"
)

type nativeThinkValue struct {
	value any
}

func (n *nativeThinkValue) UnmarshalJSON(data []byte) error {
	var boolean bool
	if err := jsonv2.Unmarshal(data, &boolean); err == nil {
		n.value = boolean
		return nil
	}
	var level string
	if err := jsonv2.Unmarshal(data, &level); err != nil {
		return errors.New("ollama: think must be a boolean or one of high, medium, low, max")
	}
	switch level {
	case "high", "medium", "low", "max":
		n.value = level
		return nil
	default:
		return fmt.Errorf("ollama: invalid think value %q, want one of high, medium, low, max", level)
	}
}

func (n nativeThinkValue) MarshalJSON() ([]byte, error) {
	return jsonv2.Marshal(n.value)
}

type nativeJSONObject struct {
	raw json.RawMessage
}

func emptyNativeJSONObject() nativeJSONObject {
	return nativeJSONObject{raw: json.RawMessage("{}")}
}

func (n *nativeJSONObject) UnmarshalJSON(data []byte) error {
	var value map[string]any
	if err := jsonv2.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == nil {
		return errors.New("must be a JSON object")
	}
	n.raw = bytes.Clone(data)
	return nil
}

func (n nativeJSONObject) MarshalJSON() ([]byte, error) {
	if len(n.raw) == 0 {
		return nil, errors.New("ollama: native JSON object is absent")
	}
	return n.raw, nil
}

type nativeImageData []byte

type nativeMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content"`
	Thinking   string            `json:"thinking,omitempty"`
	Images     []nativeImageData `json:"images,omitempty"`
	ToolCalls  []nativeToolCall  `json:"tool_calls,omitempty"`
	ToolName   string            `json:"tool_name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

func (n *nativeMessage) UnmarshalJSON(data []byte) error {
	type alias nativeMessage
	var decoded alias
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*n = nativeMessage(decoded)
	n.Role = strings.ToLower(n.Role)
	return nil
}

type nativeToolCall struct {
	ID       string                 `json:"id,omitempty"`
	Function nativeToolCallFunction `json:"function"`
}

type nativeToolCallFunction struct {
	Index     *int             `json:"index"`
	Name      string           `json:"name"`
	Arguments nativeJSONObject `json:"arguments"`
}

type nativeTools []nativeTool

type nativeTool struct {
	Type     string             `json:"type"`
	Items    any                `json:"items,omitzero"`
	Function nativeToolFunction `json:"function"`
}

type nativeToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type nativeChatRequest struct {
	Model           string                     `json:"model"`
	Messages        []nativeMessage            `json:"messages"`
	Stream          *bool                      `json:"stream,omitzero"`
	Format          json.RawMessage            `json:"format,omitzero"`
	KeepAlive       *nativeDuration            `json:"keep_alive,omitzero"`
	Tools           nativeTools                `json:"tools,omitempty"`
	Options         map[string]json.RawMessage `json:"options"`
	Think           *nativeThinkValue          `json:"think,omitzero"`
	Truncate        *bool                      `json:"truncate,omitzero"`
	Shift           *bool                      `json:"shift,omitzero"`
	DebugRenderOnly bool                       `json:"_debug_render_only,omitzero"`
	Logprobs        bool                       `json:"logprobs,omitzero"`
	TopLogprobs     int                        `json:"top_logprobs,omitzero"`
}

type nativeMetrics struct {
	TotalDuration      int64 `json:"total_duration,omitzero"`
	LoadDuration       int64 `json:"load_duration,omitzero"`
	PromptEvalCount    *int  `json:"prompt_eval_count,omitzero"`
	PromptEvalDuration int64 `json:"prompt_eval_duration,omitzero"`
	EvalCount          *int  `json:"eval_count,omitzero"`
	EvalDuration       int64 `json:"eval_duration,omitzero"`
}

type protocolDurations struct {
	Total      int64 `json:"total"`
	Load       int64 `json:"load"`
	PromptEval int64 `json:"prompt_eval"`
	Eval       int64 `json:"eval"`
}

func (n nativeMetrics) hasDurations() bool {
	return n.TotalDuration != 0 || n.LoadDuration != 0 ||
		n.PromptEvalDuration != 0 || n.EvalDuration != 0
}

type nativeChatResponse struct {
	Error       string          `json:"error,omitempty"`
	Model       string          `json:"model"`
	RemoteModel string          `json:"remote_model,omitempty"`
	RemoteHost  string          `json:"remote_host,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	Message     nativeMessage   `json:"message"`
	Done        bool            `json:"done"`
	DoneReason  string          `json:"done_reason,omitempty"`
	DebugInfo   json.RawMessage `json:"_debug_info,omitzero"`
	Logprobs    json.RawMessage `json:"logprobs,omitzero"`
	nativeMetrics
	raw json.RawMessage
}

func (n *nativeChatResponse) UnmarshalJSON(data []byte) error {
	type alias nativeChatResponse
	var decoded alias
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*n = nativeChatResponse(decoded)
	n.raw = bytes.Clone(data)
	return nil
}

func (n nativeChatResponse) metadata(resolvedModel string) (*corechat.ResponseMetadata, error) {
	metadata := &corechat.ResponseMetadata{
		Model: resolvedModel,
	}
	if n.PromptEvalCount != nil && n.EvalCount != nil {
		metadata.Usage = &corechat.Usage{InputTokens: int64(*n.PromptEvalCount), OutputTokens: int64(*n.EvalCount)}
	}
	if err := metadata.Extra.Set(ResponseExtensionKey, n.raw); err != nil {
		return nil, fmt.Errorf("ollama: preserve native response: %w", err)
	}
	if !n.CreatedAt.IsZero() {
		metadata.CreatedAt = n.CreatedAt.UTC()
	}
	if n.hasDurations() {
		durations := protocolDurations{
			Total:      n.TotalDuration,
			Load:       n.LoadDuration,
			PromptEval: n.PromptEvalDuration,
			Eval:       n.EvalDuration,
		}
		if err := metadata.Extra.Set(protocolDurationsKey, durations); err != nil {
			return nil, err
		}
	}
	if n.PromptEvalCount != nil && n.EvalCount != nil {
		metrics := protocolMetrics{
			PromptEvalCount: *n.PromptEvalCount,
			EvalCount:       *n.EvalCount,
		}
		if err := metadata.Extra.Set(protocolMetricsKey, metrics); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

type nativeEmbedRequest struct {
	Model      string                     `json:"model"`
	Input      any                        `json:"input"`
	KeepAlive  *nativeDuration            `json:"keep_alive,omitzero"`
	Truncate   *bool                      `json:"truncate,omitzero"`
	Dimensions int                        `json:"dimensions,omitzero"`
	Options    map[string]json.RawMessage `json:"options"`
}

type nativeEmbedResponse struct {
	Model           string                            `json:"model"`
	Embeddings      []corevector.Coordinates[float64] `json:"embeddings"`
	TotalDuration   *int64                            `json:"total_duration,omitzero"`
	LoadDuration    *int64                            `json:"load_duration,omitzero"`
	PromptEvalCount *int64                            `json:"prompt_eval_count,omitzero"`
}
