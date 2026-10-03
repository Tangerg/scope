package agent

import (
	"testing"
)

func TestChildStartEncodingFailureDoesNotCreateUnknown(t *testing.T) {
	id := controlValue(ParseEffectID("effect:child-start"))
	record := preparedEffect{ID: id, progress: &effectProgress{}}
	err := record.settleOperation(childStartOperation{}, Failure{})
	if err == nil {
		t.Fatalf("encoding error = %v", err)
	}
	if record.phase() != effectPhasePending || record.settlement() != nil {
		t.Fatalf("encoding failure settled child start: %+v", record)
	}
}
