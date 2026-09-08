package chroma

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestVisitor_Conformance(t *testing.T) {
	storetest.VisitorConformance(t,
		func(src string) error {
			expr, err := filter.Parse(src)
			if err != nil {
				return err
			}
			v := newVisitor()
			return expr.Accept(v)
		},
		storetest.Options{Unsupported: []string{
			"not", "nested_logical", "collection_membership", "like", "null_test", "not_null_test"}},
	)
}

func TestVisitor_RejectsLossyNumbers(t *testing.T) {
	tests := map[string]filter.Predicate{
		"integer outside int": filter.EQ("id", uint64(^uint(0))),
		"mixed list rounds integer": filter.In("id", []*filter.Literal{
			filter.NewLiteral(1<<24 + 1),
			filter.NewLiteral(1.5),
		}),
	}
	for name, predicate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := predicate.Accept(newVisitor()); err == nil {
				t.Fatal("Chroma silently accepted a lossy number")
			}
		})
	}
}
