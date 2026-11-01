package anthropic

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/Tangerg/scope/core/chat"
)

func TestMessagesUsageCombinesOnlyReportedCounters(t *testing.T) {
	var native messagesUsage
	for _, tc := range []struct {
		raw           string
		known         bool
		input, output int64
	}{
		{`{}`, false, 0, 0}, {`{"input_tokens":5,"cache_read_input_tokens":4}`, false, 0, 0}, {`{"output_tokens":2}`, true, 9, 2},
		{`{}`, true, 9, 2}, {`{"output_tokens":3}`, true, 9, 3}, {`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0}`, true, 0, 0},
	} {
		if err := native.observe(tc.raw, &chat.ResponseMetadata{}); err != nil {
			t.Fatal(err)
		}
		got, err := native.snapshot()
		if err != nil || (got != nil) != tc.known || got != nil && (got.InputTokens != tc.input || got.OutputTokens != tc.output) {
			t.Fatalf("%s snapshot=%+v, %v", tc.raw, got, err)
		}
	}
}

func TestStreamUsageBelongsToItsNativeReportAndEachYieldOwnsItsSnapshot(t *testing.T) {
	state := newProtocolStreamState("anthropic")
	read := func(raw string) *chat.ResponseDelta {
		t.Helper()
		var event anthropicsdk.MessageStreamEventUnion
		if err := jsonv2.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		delta, err := state.mapEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		return delta
	}
	start := read(`{"type":"message_start","message":{"id":"msg","model":"claude","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":8,"cache_read_input_tokens":3,"output_tokens_details":{"thinking_tokens":1}}}}`)
	if start.Metadata.Usage == nil || start.Metadata.Usage.InputTokens != 13 || start.Metadata.Usage.OutputTokens != 8 || start.Metadata.Usage.CacheReadInputTokens == nil || start.Metadata.Usage.ReasoningTokens == nil {
		t.Fatalf("start usage=%+v", start.Metadata.Usage)
	}
	*start.Metadata.Usage.CacheReadInputTokens = 111
	*start.Metadata.Usage.ReasoningTokens = 222
	missing := read(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	if got := missing.Metadata.Usage; got == nil || got.InputTokens != 13 || got.OutputTokens != 8 || *got.CacheReadInputTokens != 3 || *got.ReasoningTokens != 1 {
		t.Fatalf("missing report or caller mutation changed accounting: %+v", got)
	}
	if _, present := missing.Metadata.Extra[protocolUsageKey]; present {
		t.Fatal("missing report was exposed as provider usage")
	}
	report := read(`{"type":"message_delta","delta":{},"usage":{"output_tokens":0,"output_tokens_details":{"thinking_tokens":0},"provider_detail":17}}`)
	if got := report.Metadata.Usage; got == nil || got.OutputTokens != 0 || *got.ReasoningTokens != 0 {
		t.Fatalf("explicit zero = %+v", got)
	}
	if got := string(report.Metadata.Extra[protocolUsageKey]); !strings.Contains(got, `"provider_detail":17`) || strings.Contains(got, `"input_tokens"`) {
		t.Fatalf("provider report was reconstructed: %s", got)
	}
}

func TestInvalidNativeUsageCannotBecomeAChargeOrAZero(t *testing.T) {
	for _, wire := range []string{`{"input_tokens":false,"output_tokens":1}`, `{"input_tokens":"4","output_tokens":1}`, `{"input_tokens":1,"output_tokens":0.5}`, `{"cache_read_input_tokens":-1}`, `{"input_tokens":9223372036854775808}`, `{"input_tokens":9223372036854775807,"output_tokens":0,"cache_creation_input_tokens":1}`} {
		var native messagesUsage
		err := native.observe(wire, &chat.ResponseMetadata{})
		if err == nil {
			_, err = native.snapshot()
		}
		if !errors.Is(err, chat.ErrInvalidResponse) {
			t.Fatalf("invalid usage %s, err=%v", wire, err)
		}
	}
}
