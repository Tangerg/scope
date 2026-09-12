package openai

import (
	"encoding/json"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
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
		if err := json.Unmarshal([]byte(test.wire), &usage); err != nil {
			t.Fatal(err)
		}
		if actual := mapUsage(usage); (actual != nil) != test.known {
			t.Fatalf("accounting %s = %+v", test.wire, actual)
		}
	}
}
