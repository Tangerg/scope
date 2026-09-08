package cassandra

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// TestVisitor_Conformance exercises every AST shape the filter DSL supports
// against this compiler via the shared suite. Output equivalence stays in the
// per-test functions; this is "every shape gets an answer" coverage, and an
// operator the backend cannot express has to be declared rather than left to
// fail by accident.
func TestVisitor_Conformance(t *testing.T) {
	storetest.VisitorConformance(t, func(src string) error {
		expr, err := filter.Parse(src)
		if err != nil {
			return err
		}
		return expr.Accept(newVisitor())
	},
		storetest.Options{
			// CQL restrictions this store cannot lift: no OR or standalone NOT
			// in a WHERE clause, no IS NULL, no LIKE on a metadata column, no
			// collection column in this scalar schema, and no indexed or
			// nested key — a metadata key has to be declared as a column.
			Unsupported: []string{
				"or", "not", "nested_logical", "null_test", "not_null_test",
				"like", "collection_membership", "indexed_key", "nested_index",
			},
		},
	)
}
