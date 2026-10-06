package anthropic

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestMessagesRequireCompleteTerminalSequence(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	blockStart := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"answer"}}`
	blockStop := `{"type":"content_block_stop","index":0}`
	finish := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`
	stop := `{"type":"message_stop"}`
	for _, test := range []struct {
		name   string
		events []string
		valid  bool
	}{
		{name: "normal", events: []string{start, blockStart, blockStop, finish, stop}, valid: true},
		{name: "ping events", events: []string{`{"type":"ping"}`, start, blockStart, `{"type":"ping"}`, blockStop, finish, stop}, valid: true},
		{name: "tail usage", events: []string{start, blockStart, blockStop, finish, `{"type":"message_delta","delta":{},"usage":{"output_tokens":2}}`, stop}, valid: true},
		{name: "missing message start", events: []string{blockStart, blockStop, finish, stop}},
		{name: "duplicate message start", events: []string{start, blockStart, blockStop, start, finish, stop}},
		{name: "unstarted text block", events: []string{start, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`, finish, stop}},
		{name: "unstarted thinking block", events: []string{start, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"answer"}}`, finish, stop}},
		{name: "tool arguments before block start", events: []string{start, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu-1","name":"lookup","input":{}}}`, blockStop, finish, stop}},
		{name: "text delta for tool block", events: []string{start, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu-1","name":"lookup","input":{}}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`, blockStop, finish, stop}},
		{name: "tool arguments for text block", events: []string{start, blockStart, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`, blockStop, finish, stop}},
		{name: "signature for text block", events: []string{start, blockStart, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`, blockStop, finish, stop}},
		{name: "negative block index", events: []string{start, `{"type":"content_block_start","index":-1,"content_block":{"type":"text","text":"answer"}}`, `{"type":"content_block_stop","index":-1}`, finish, stop}},
		{name: "missing message stop", events: []string{start, blockStart, blockStop, finish}},
		{name: "missing block stop", events: []string{start, blockStart, finish, stop}},
		{name: "missing finish reason", events: []string{start, blockStart, blockStop, stop}},
		{name: "duplicate message stop", events: []string{start, blockStart, blockStop, finish, stop, stop}},
		{name: "content after finish", events: []string{start, blockStart, blockStop, finish, blockStart, stop}},
		{name: "content after stop", events: []string{start, blockStart, blockStop, finish, stop, blockStart}},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newLifecycleMessages(t, test.events)
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, err := model.Call(t.Context(), request)
			if !test.valid {
				if !errors.Is(err, corechat.ErrInvalidResponse) || response != nil {
					t.Fatalf("Call = %#v, %v; want invalid response", response, err)
				}
				terminals := 0
				err = nil
				for delta, streamErr := range model.Stream(t.Context(), request) {
					if streamErr != nil {
						err = streamErr
						break
					}
					if delta.FinishReason != "" {
						terminals++
					}
				}
				if !errors.Is(err, corechat.ErrInvalidResponse) || terminals != 0 {
					t.Fatalf("Stream produced %d terminal deltas and error %v", terminals, err)
				}
				return
			}
			if err != nil || response.Text() != "answer" || response.Output.FinishReason != corechat.FinishReasonStop {
				t.Fatalf("Call = %#v, %v", response, err)
			}
			wantTokens := int64(1)
			if test.name == "tail usage" {
				wantTokens = 2
			}
			if response.Metadata.Usage.OutputTokens != wantTokens {
				t.Fatalf("output tokens = %d, want %d", response.Metadata.Usage.OutputTokens, wantTokens)
			}
		})
	}
}

func TestMessagesPreserveNativeServerToolBlocksWithoutPromotingLocalCalls(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu-1","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"weather\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"future_tool","id":"future-1","name":"remote","input":{},"opaque_extension":"preserved"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"answer"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	}
	model := newLifecycleMessages(t, events)
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Text() != "answer" || len(response.Output.Message.Parts) != 1 || response.Output.FinishReason != corechat.FinishReasonStop {
		t.Fatalf("native server tool promoted into Core calls: %#v", response)
	}
	var nativeEvents []string
	for delta, streamErr := range model.Stream(t.Context(), request) {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		for _, part := range delta.Parts {
			if part.Kind == corechat.PartDeltaToolCall {
				t.Fatalf("native server tool promoted into a local call: %#v", part)
			}
		}
		native, found, decodeErr := delta.Metadata.Extra.Decode[json.RawMessage](StreamEventExtensionKey)
		if decodeErr != nil || !found {
			t.Fatalf("preserved native event: found = %v, error = %v", found, decodeErr)
		}
		nativeEvents = append(nativeEvents, string(native))
	}
	if len(nativeEvents) != len(events) {
		t.Fatalf("preserved %d native events, want %d", len(nativeEvents), len(events))
	}
	for index, event := range events {
		var want, got any
		if unmarshalErr := jsonv2.Unmarshal([]byte(event), &want); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		if unmarshalErr := jsonv2.Unmarshal([]byte(nativeEvents[index]), &got); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("native event %d = %#v, want %#v", index, got, want)
		}
	}
}

func TestMessagesReasoningBlocksRetainIdentityThroughHistory(t *testing.T) {
	for _, redacted := range []bool{false, true} {
		name := "signed"
		if redacted {
			name = "redacted"
		}
		t.Run(name, func(t *testing.T) {
			events := []string{`{"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`}
			for index, text := range []string{"first", "second"} {
				if redacted {
					events = append(events, fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"redacted_thinking","data":"opaque-%s"}}`, index, text))
				} else {
					events = append(events,
						fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, index),
						fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":%q}}`, index, text),
						fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":"sig-"}}`, index),
						fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":%q}}`, index, text),
					)
				}
				events = append(events, fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index))
			}
			events = append(events, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, `{"type":"message_stop"}`)
			model := newLifecycleMessages(t, events)
			response, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := jsonv2.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var restored corechat.Response
			if unmarshalErr := jsonv2.Unmarshal(encoded, &restored); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			params, err := model.buildProtocolRequest(&corechat.Request{Messages: []corechat.Message{*restored.Output.Message}})
			if err != nil {
				t.Fatal(err)
			}
			blocks := params.Messages[0].Content
			if len(blocks) != 2 {
				t.Fatalf("replayed %d thinking blocks, want 2: %#v", len(blocks), restored.Output.Message.Parts)
			}
			for index, text := range []string{"first", "second"} {
				if redacted {
					if block := blocks[index].OfRedactedThinking; block == nil || block.Data != "opaque-"+text {
						t.Fatalf("redacted block %d = %#v", index, block)
					}
					continue
				}
				if block := blocks[index].OfThinking; block == nil || block.Thinking != text || block.Signature != "sig-"+text {
					t.Fatalf("signed block %d = %#v", index, block)
				}
			}
		})
	}
}

func newLifecycleMessages(t *testing.T, events []string) *Messages {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			var envelope struct {
				Type string `json:"type"`
			}
			if err := jsonv2.Unmarshal([]byte(event), &envelope); err != nil {
				t.Error(err)
				return
			}
			fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", envelope.Type, event)
		}
	}))
	t.Cleanup(server.Close)
	model, err := NewMessages(t.Context(), MessagesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "claude-test"}})
	if err != nil {
		t.Fatal(err)
	}
	return model
}
