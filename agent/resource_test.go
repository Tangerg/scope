package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"
)

func TestUsageDecodeRequiresEveryCounter(t *testing.T) {
	original := Usage{CommittedSteps: 7, PreparedEffects: 5, AcceptedSignals: 3, DroppedDeltas: 2}
	for _, usage := range []Usage{{}, original} {
		var decoded Usage
		if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(usage)), &decoded); err != nil || decoded != usage {
			t.Fatalf("usage round trip = %+v, %v; want %+v", decoded, err, usage)
		}
	}
	for _, name := range []string{"committed_steps", "prepared_effects", "accepted_signals", "dropped_deltas"} {
		for _, missing := range []bool{true, false} {
			var fields map[string]json.RawMessage
			if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(original)), &fields); err != nil {
				t.Fatal(err)
			}
			if missing {
				delete(fields, name)
			} else {
				fields[name] = json.RawMessage(`null`)
			}
			decoded := original
			if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(fields)), &decoded); err == nil {
				t.Errorf("incomplete %s (missing=%t) accepted as %+v", name, missing, decoded)
			}
			if decoded != original {
				t.Errorf("rejected %s changed prior usage: %+v", name, decoded)
			}
		}
	}
}

func TestResourceQuantitiesFitWithoutUnsignedOverflow(t *testing.T) {
	const maxUint64 = ^uint64(0)
	tests := []struct {
		name       string
		limit      uint64
		quantities []uint64
		want       bool
	}{
		{name: "exact boundary", limit: maxUint64, quantities: []uint64{maxUint64 - 1, 1}, want: true},
		{name: "overflowing sum", limit: maxUint64, quantities: []uint64{maxUint64 - 1, 1, 1}},
		{name: "multiple reservations fit", limit: maxUint64, quantities: []uint64{maxUint64 - 2, 1, 1}, want: true},
		{name: "ordinary over limit", limit: 10, quantities: []uint64{7, 4}},
		{name: "ordinary within limit", limit: 10, quantities: []uint64{7, 3}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resourceQuantitiesFit(test.limit, test.quantities...); got != test.want {
				t.Fatalf("resourceQuantitiesFit(%d, %v) = %t, want %t", test.limit, test.quantities, got, test.want)
			}
		})
	}
}

func TestSaturatingCountAddPreservesMonotonicity(t *testing.T) {
	const maxUint64 = ^uint64(0)
	if got := saturatingCountAdd(maxUint64-1, 2); got != maxUint64 {
		t.Fatalf("saturatingCountAdd() = %d, want %d", got, uint64(maxUint64))
	}
}
