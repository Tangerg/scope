package openai

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"

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
		if actual, mapErr := mapUsage(usage); mapErr != nil || (actual != nil) != test.known {
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
		actual, err := responsesUsage(usage)
		if err != nil {
			t.Fatal(err)
		}
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

func TestUsageRejectsCoercedProviderCounters(t *testing.T) {
	for _, amount := range []string{`true`, `"4"`, `0.5`, `9223372036854775808`, `-1`} {
		var chatUsage openaisdk.CompletionUsage
		var responsesUsageValue responses.ResponseUsage
		if err := jsonv2.Unmarshal([]byte(`{"prompt_tokens":4,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":`+amount+`}}`), &chatUsage); err != nil {
			t.Fatal(err)
		}
		if err := jsonv2.Unmarshal([]byte(`{"input_tokens":`+amount+`,"output_tokens":1}`), &responsesUsageValue); err != nil {
			t.Fatal(err)
		}
		for _, project := range []func() (*chat.Usage, error){func() (*chat.Usage, error) { return mapUsage(chatUsage) }, func() (*chat.Usage, error) { return responsesUsage(responsesUsageValue) }} {
			if got, err := project(); got != nil || !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("malformed usage %s became %+v, %v", amount, got, err)
			}
		}
	}
}
