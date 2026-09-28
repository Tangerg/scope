package cassandra

import (
	"math"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestVisitor_PreservesUnsignedIntegerList(t *testing.T) {
	visitor := newVisitor([]MetadataColumn{{Name: "id", CQLType: "varint"}})
	if err := filter.In("id", []uint64{math.MaxUint64}).Accept(visitor); err != nil {
		t.Fatal(err)
	}
	_, args := visitor.snapshot()
	if len(args) != 1 {
		t.Fatalf("args = %#v", args)
	}
	values, ok := args[0].([]uint64)
	if !ok || len(values) != 1 || values[0] != math.MaxUint64 {
		t.Fatalf("IN argument = %#v (%T)", args[0], args[0])
	}
}
