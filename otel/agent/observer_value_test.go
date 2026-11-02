package agent

import (
	"math"
	"testing"
)

func TestUint64AttributePreservesFullRange(t *testing.T) {
	attribute := uint64Attribute("agent.test.sequence", math.MaxUint64)
	if got := attribute.Value.AsString(); got != "18446744073709551615" {
		t.Fatalf("uint64 attribute = %q", got)
	}
}
