package anthropic

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestMessagesRejectContentThatCannotSurviveHistoryReplay(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	text := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"observed"}}`
	closeText := `{"type":"content_block_stop","index":0}`
	finish := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`
	stop := `{"type":"message_stop"}`
	for _, test := range []struct {
		name        string
		events      []string
		problem     string
		visibleText string
	}{
		{
			name: "server tool", problem: "server_tool_use", visibleText: "observed",
			events: []string{start, text, closeText, `{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu-1","name":"web_search","input":{}}}`, `{"type":"content_block_stop","index":1}`, finish, stop},
		},
		{
			name: "server result", problem: "web_search_tool_result", visibleText: "observed",
			events: []string{start, text, closeText, `{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu-1","content":[]}}`, `{"type":"content_block_stop","index":1}`, finish, stop},
		},
		{
			name: "future content", problem: "future_tool", visibleText: "observed",
			events: []string{start, text, closeText, `{"type":"content_block_start","index":1,"content_block":{"type":"future_tool","id":"future-1","opaque":"state"}}`, `{"type":"content_block_stop","index":1}`, finish, stop},
		},
		{
			name: "paused turn", problem: "pause_turn", visibleText: "observed",
			events: []string{start, text, closeText, `{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":1}}`, stop},
		},
		{
			name: "future delta", problem: "future_delta", visibleText: "observed",
			events: []string{start, text, `{"type":"content_block_delta","index":0,"delta":{"type":"future_delta","opaque":"state"}}`, closeText, finish, stop},
		},
		{
			name: "initial server tool", problem: "server_tool_use",
			events: []string{fmt.Sprintf(`{"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[%s],"usage":{"input_tokens":1,"output_tokens":0}}}`, `{"type":"server_tool_use","id":"srvtoolu-1","name":"web_search","input":{}}`), finish, stop},
		},
		{
			name: "initial future content", problem: "future_tool",
			events: []string{fmt.Sprintf(`{"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[%s],"usage":{"input_tokens":1,"output_tokens":0}}}`, `{"type":"future_tool","opaque":"state"}`), finish, stop},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newLifecycleMessages(t, test.events)
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, err := model.Call(t.Context(), request)
			if response != nil || !errors.Is(err, errors.ErrUnsupported) || !strings.Contains(err.Error(), test.problem) {
				t.Fatalf("Call = %#v, %v; want unsupported %s", response, err, test.problem)
			}
			var visible strings.Builder
			var streamFailure error
			for delta, streamErr := range model.Stream(t.Context(), request) {
				if streamErr != nil {
					streamFailure = streamErr
					break
				}
				if delta.FinishReason != "" {
					t.Fatalf("unsupported content produced terminal reason %q", delta.FinishReason)
				}
				for _, part := range delta.Parts {
					if part.Kind == corechat.PartDeltaToolCall {
						t.Fatalf("remote content became a local tool call: %#v", part)
					}
					if part.Kind == corechat.PartDeltaText {
						visible.WriteString(part.Text)
					}
				}
			}
			if !errors.Is(streamFailure, errors.ErrUnsupported) || corechat.ClassifyFailure(streamFailure) == corechat.CallFailureNotExecuted || visible.String() != test.visibleText {
				t.Fatalf("Stream text = %q, error = %v; want %q and executed unsupported failure", visible.String(), streamFailure, test.visibleText)
			}
		})
	}
}
