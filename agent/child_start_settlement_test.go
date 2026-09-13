package agent

import (
	"testing"
)

func TestChildStartEncodingFailureDoesNotCreateUnknown(t *testing.T) {
	id := controlValue(ParseEffectID("effect:child-start"))
	record := preparedEffect{ID: id, Phase: effectPhasePending}
	err := record.settleChildStart(ChildStartResult{})
	if err == nil {
		t.Fatalf("encoding error = %v", err)
	}
	if record.Phase != effectPhasePending || record.Settlement != nil {
		t.Fatalf("encoding failure settled child start: %+v", record)
	}
}
