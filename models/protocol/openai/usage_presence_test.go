package openai

import (
	jsonv2 "encoding/json/v2"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func TestCompletionUsageRequiresReportedTotals(t *testing.T) {
	for _, test := range []struct {
		wire  string
		known bool
	}{
		{`{}`, false}, {`{"prompt_tokens":3}`, false},
		{`{"prompt_tokens":0,"completion_tokens":0}`, true},
		{`{"prompt_tokens":3,"completion_tokens":2}`, true},
	} {
		var usage openaisdk.CompletionUsage
		if err := jsonv2.Unmarshal([]byte(test.wire), &usage); err != nil {
			t.Fatal(err)
		}
		if actual := mapUsage(usage); (actual != nil) != test.known {
			t.Fatalf("accounting %s = %+v", test.wire, actual)
		}
	}
}

func TestResponsesUsagePreservesReportedZeroDetails(t *testing.T) {
	for _, test := range []struct {
		details string
		known   bool
		tokens  int64
	}{
		{"", false, 0},
		{`,"input_tokens_details":{},"output_tokens_details":{}`, false, 0},
		{`,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}`, true, 0},
		{`,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":2},"output_tokens_details":{"reasoning_tokens":2}`, true, 2},
	} {
		var usage responses.ResponseUsage
		if err := jsonv2.Unmarshal([]byte(`{"input_tokens":3,"output_tokens":4`+test.details+`}`), &usage); err != nil {
			t.Fatal(err)
		}
		actual := responsesUsage(usage)
		if actual == nil {
			t.Fatal("reported usage is unknown")
		}
		for name, value := range map[string]*int64{"reasoning": actual.ReasoningTokens, "cache_read": actual.CacheReadInputTokens, "cache_write": actual.CacheWriteInputTokens} {
			if (value != nil) != test.known || value != nil && *value != test.tokens {
				t.Fatalf("%s = %v for %s", name, value, test.details)
			}
		}
	}
}
