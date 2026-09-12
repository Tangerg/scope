package anthropic

import (
	"encoding/json"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/Tangerg/scope/core/chat"
)

func TestUsageRequiresReportedTotals(t *testing.T) {
	for _, sample := range []struct {
		wire  string
		known bool
	}{
		{`{}`, false}, {`{"input_tokens":3}`, false},
		{`{"input_tokens":0,"output_tokens":0}`, true},
		{`{"input_tokens":3,"output_tokens":2}`, true},
	} {
		var usage anthropicsdk.Usage
		if err := json.Unmarshal([]byte(sample.wire), &usage); err != nil {
			t.Fatal(err)
		}
		if actual := mapProtocolUsage(usage); (actual != nil) != sample.known {
			t.Fatalf("accounting %s = %+v", sample.wire, actual)
		}
	}
}

func TestStreamingUsagePreservesOmittedCountersAndAcceptsZero(t *testing.T) {
	state := protocolStreamState{usage: &chat.Usage{InputTokens: 9, OutputTokens: 2, CacheReadInputTokens: new(int64(4))}}
	for _, sample := range []struct {
		wire   string
		input  int64
		output int64
	}{
		{`{}`, 9, 2},
		{`{"output_tokens":3}`, 9, 3},
		{`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0}`, 0, 0},
	} {
		var delta anthropicsdk.MessageDeltaUsage
		if err := json.Unmarshal([]byte(sample.wire), &delta); err != nil {
			t.Fatal(err)
		}
		state.mergeDeltaUsage(delta)
		if state.usage.InputTokens != sample.input || state.usage.OutputTokens != sample.output {
			t.Fatalf("after %s: %+v", sample.wire, state.usage)
		}
	}
	var partial anthropicsdk.MessageDeltaUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":0}`), &partial); err != nil {
		t.Fatal(err)
	}
	unknown := protocolStreamState{}
	unknown.mergeDeltaUsage(partial)
	if unknown.usage != nil {
		t.Fatal("partial report established complete accounting")
	}
}
