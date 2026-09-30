package tidb

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
		return expr.Accept(newVisitor("metadata"))
	}, storetest.Options{
		// The backend has no array element access with filter.Match semantics.
		Unsupported: []string{"array_index", "nested_array_index", "array_index_key"},
	})
}
