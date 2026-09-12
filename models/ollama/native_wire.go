package ollama

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	corechat "github.com/Tangerg/scope/core/chat"
)

type nativeDuration struct {
	time.Duration
}

func (n nativeDuration) MarshalJSON() ([]byte, error) {
	if n.Duration < 0 {
		return []byte("-1"), nil
	}
	return json.Marshal(n.String())
}

func (n *nativeDuration) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch typed := value.(type) {
	case float64:
		if typed < 0 {
			n.Duration = time.Duration(math.MaxInt64)
			return nil
		}
		n.Duration = time.Duration(typed * float64(time.Second))
		return nil
	case string:
		parsed, err := time.ParseDuration(typed)
		if err != nil {
			return err
		}
		if parsed < 0 {
			parsed = time.Duration(math.MaxInt64)
		}
		n.Duration = parsed
		return nil
	default:
		return errors.New("ollama: duration must be a number of seconds or duration string")
	}
}

type nativeThinkValue struct {
	value any
}

// newNativeThinkLevel builds a think value from a thinking level, which is the
// vocabulary /api/chat documents alongside the boolean form: "low", "medium",
// "high" or "max". It owns that list so the wire decoder and the option mapping
// cannot disagree about what the daemon accepts.
func newNativeThinkLevel(level string) (*nativeThinkValue, error) {
	switch level {
	case "high", "medium", "low", "max":
		return &nativeThinkValue{value: level}, nil
	default:
		return nil, fmt.Errorf("ollama: invalid think value %q, want one of high, medium, low, max", level)
	}
}

func (n *nativeThinkValue) UnmarshalJSON(data []byte) error {
	var boolean bool
	if err := json.Unmarshal(data, &boolean); err == nil {
		n.value = boolean
		return nil
	}
	var level string
	if err := json.Unmarshal(data, &level); err != nil {
		return errors.New("ollama: think must be a boolean or one of high, medium, low, max")
	}
	value, err := newNativeThinkLevel(level)
	if err != nil {
		return err
	}
	*n = *value
	return nil
}

func (n nativeThinkValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(n.value)
}

type nativeJSONObject struct {
	raw json.RawMessage
}

func emptyNativeJSONObject() nativeJSONObject {
	return nativeJSONObject{raw: json.RawMessage("{}")}
}

func (n *nativeJSONObject) UnmarshalJSON(data []byte) error {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
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
		return []byte("{}"), nil
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
	if err := json.Unmarshal(data, &decoded); err != nil {
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
	Index     int              `json:"index"`
	Name      string           `json:"name"`
	Arguments nativeJSONObject `json:"arguments"`
}

type nativeTools []nativeTool

type nativeTool struct {
	Type     string             `json:"type"`
	Items    any                `json:"items,omitempty"`
	Function nativeToolFunction `json:"function"`
}

type nativeToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type nativeChatRequest struct {
	Model           string            `json:"model"`
	Messages        []nativeMessage   `json:"messages"`
	Stream          *bool             `json:"stream,omitempty"`
	Format          json.RawMessage   `json:"format,omitempty"`
	KeepAlive       *nativeDuration   `json:"keep_alive,omitempty"`
	Tools           nativeTools       `json:"tools,omitempty"`
	Options         map[string]any    `json:"options"`
	Think           *nativeThinkValue `json:"think,omitempty"`
	Truncate        *bool             `json:"truncate,omitempty"`
	Shift           *bool             `json:"shift,omitempty"`
	DebugRenderOnly bool              `json:"_debug_render_only,omitempty"`
	Logprobs        bool              `json:"logprobs,omitempty"`
	TopLogprobs     int               `json:"top_logprobs,omitempty"`
}

type nativeMetrics struct {
	TotalDuration      time.Duration `json:"total_duration,omitempty"`
	LoadDuration       time.Duration `json:"load_duration,omitempty"`
	PromptEvalCount    int           `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration time.Duration `json:"prompt_eval_duration,omitempty"`
	EvalCount          int           `json:"eval_count,omitempty"`
	EvalDuration       time.Duration `json:"eval_duration,omitempty"`
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
	DebugInfo   json.RawMessage `json:"_debug_info,omitempty"`
	Logprobs    json.RawMessage `json:"logprobs,omitempty"`
	nativeMetrics
	raw json.RawMessage
}

func (n *nativeChatResponse) UnmarshalJSON(data []byte) error {
	type alias nativeChatResponse
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*n = nativeChatResponse(decoded)
	n.raw = bytes.Clone(data)
	return nil
}

func (n nativeChatResponse) metadata(requestModel string) (*corechat.ResponseMetadata, error) {
	modelName := n.Model
	if modelName == "" {
		modelName = requestModel
	}
	metadata := &corechat.ResponseMetadata{
		Model: modelName,
		Usage: corechat.Usage{
			InputTokens:  int64(n.PromptEvalCount),
			OutputTokens: int64(n.EvalCount),
		},
	}
	if err := metadata.Extra.Set(ResponseExtensionKey, n.raw); err != nil {
		return nil, fmt.Errorf("ollama: preserve native response: %w", err)
	}
	if !n.CreatedAt.IsZero() {
		metadata.CreatedAt = n.CreatedAt.UTC()
	}
	if n.hasDurations() {
		durations := map[string]int64{
			"total":       int64(n.TotalDuration),
			"load":        int64(n.LoadDuration),
			"prompt_eval": int64(n.PromptEvalDuration),
			"eval":        int64(n.EvalDuration),
		}
		if err := metadata.Extra.Set(protocolDurationsKey, durations); err != nil {
			return nil, err
		}
	}
	if n.PromptEvalCount != 0 || n.EvalCount != 0 {
		metrics := protocolMetrics{
			PromptEvalCount: n.PromptEvalCount,
			EvalCount:       n.EvalCount,
		}
		if err := metadata.Extra.Set(protocolMetricsKey, metrics); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

type nativeEmbedRequest struct {
	Model      string          `json:"model"`
	Input      any             `json:"input"`
	KeepAlive  *nativeDuration `json:"keep_alive,omitempty"`
	Truncate   *bool           `json:"truncate,omitempty"`
	Dimensions int             `json:"dimensions,omitempty"`
	Options    map[string]any  `json:"options"`
}

type nativeEmbedResponse struct {
	Model           string        `json:"model"`
	Embeddings      [][]float32   `json:"embeddings"`
	TotalDuration   time.Duration `json:"total_duration,omitempty"`
	LoadDuration    time.Duration `json:"load_duration,omitempty"`
	PromptEvalCount int           `json:"prompt_eval_count,omitempty"`
}
