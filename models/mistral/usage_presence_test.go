package mistral

import (
	jsonv2 "encoding/json/v2"
	"testing"
)

func TestUsageRequiresBothReportedTotals(t *testing.T) {
	for _, sample := range []struct {
		wire  string
		known bool
	}{
		{`null`, false}, {`{}`, false}, {`{"prompt_tokens":3}`, false},
		{`{"prompt_tokens":0,"completion_tokens":0}`, true},
		{`{"prompt_tokens":3,"completion_tokens":2}`, true},
	} {
		var report *chatUsage
		if err := jsonv2.Unmarshal([]byte(sample.wire), &report); err != nil {
			t.Fatal(err)
		}
		if actual := report.usage(); (actual != nil) != sample.known {
			t.Fatalf("accounting %s = %+v", sample.wire, actual)
		}
	}
}

func TestCachedUsagePreservesZero(t *testing.T) {
	for _, test := range []struct {
		details string
		known   bool
		tokens  int64
	}{
		{"", false, 0}, {`,"num_cached_tokens":0`, true, 0}, {`,"prompt_tokens_details":{}`, false, 0},
		{`,"prompt_tokens_details":{"cached_tokens":0}`, true, 0},
		{`,"num_cached_tokens":3`, true, 3}, {`,"prompt_tokens_details":{"cached_tokens":4}`, true, 4},
	} {
		var report chatUsage
		if err := jsonv2.Unmarshal([]byte(`{"prompt_tokens":5,"completion_tokens":1`+test.details+`}`), &report); err != nil {
			t.Fatal(err)
		}
		usage := report.usage()
		if usage == nil {
			t.Fatal("reported usage missing")
		}
		value := usage.CacheReadInputTokens
		if (value != nil) != test.known || value != nil && *value != test.tokens {
			t.Fatalf("cache usage=%v for %s", value, test.details)
		}
	}
}
