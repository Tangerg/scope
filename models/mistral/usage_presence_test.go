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
