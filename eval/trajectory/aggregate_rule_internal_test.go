package trajectory

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/eval"
)

func TestAggregateDecisionIdentifiesConstrainedMetrics(t *testing.T) {
	aggregate := func(name eval.MetricName) string {
		t.Helper()
		detail, err := measurementReport(name, metricUnitCount, 1, false, uint64(0))
		if err != nil {
			t.Fatal(err)
		}
		report, err := allExpectationsReport([]eval.Report{detail})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := jsonv2.Marshal(report.Decision.Parameters)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	steps, effects := aggregate(MetricCommittedSteps), aggregate(MetricPreparedEffects)
	if steps == effects {
		t.Fatalf("limits on different metrics share one aggregate rule: %s", steps)
	}
	if again := aggregate(MetricCommittedSteps); again != steps {
		t.Fatalf("the same limit produced different aggregate rules: %s and %s", steps, again)
	}
}
