package vectara

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
	},
		storetest.Options{
			// Vectara filterable metadata fields are scalar, so there is no
			// collection to test membership against.
			Unsupported: []string{"collection_membership"},
			// This compiler writes a metadata key into the query language as text,
			// so a key that language cannot name is refused rather than approximated.
			InterpolatesKeyPaths: true,
			CompileText:          compileFilterText,
		},
	)
}

// compileFilterText drives the compiler and returns the query text it produced,
// so the shared suite can require the exact digits of a numeric literal. This
// compiler's whole output is text, which is what makes those digits the only
// thing between a caller's filter and a different one.
func compileFilterText(source string) (string, error) {
	expr, err := filter.Parse(source)
	if err != nil {
		return "", err
	}
	compiler := newVisitor("metadata")
	if err := expr.Accept(compiler); err != nil {
		return "", err
	}
	return compiler.snapshot(), nil
}
