package azureaisearch

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
			// $filter has no string function to build a pattern match on, and
			// it does not support nested-property paths.
			Unsupported: []string{"like", "indexed_key", "nested_index"},
			// This compiler writes a metadata key into the query language as text,
			// so a key that language cannot name is refused rather than approximated.
			InterpolatesKeyPaths: true,
		},
	)
}
