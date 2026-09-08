// Package storetest contains provider-independent contract tests for
// vector-store implementations and their filter visitors.
//
// [Run] verifies the exact capability set and validation boundary of a store,
// comparing the set the store declares against the set [CapabilitiesOf]
// detects. Exact means both directions: a capability the store implements but
// does not declare fails just as a declared one it does not implement does,
// which is what keeps a no-op [vectorstore.Closer] from passing as cleanup.
// [VisitorConformance] exercises the common filter AST shapes, while
// [VisitorLifecycle] verifies that a visitor can be safely reused.
//
// Each vendor wires the suite up in a single test file:
//
//	func TestVisitor_Conformance(t *testing.T) {
//	    storetest.VisitorConformance(t, func(src string) error {
//	        expr, err := filter.Parse(src)
//	        if err != nil {
//	            return err
//	        }
//	        compiler := newVisitor(myFieldSchema)
//	        return expr.Accept(compiler)
//	    })
//	}
//
// Output equivalence (the actual emitted SQL / filter struct) is NOT
// covered by the suite — backends emit heterogeneous output types and
// the vendor's own tests still own that responsibility. The suite
// only guarantees "every valid AST shape visits without error; every
// well-known invalid AST shape produces an error".
//
// # Field identifiers
//
// Every success case uses a disjoint field name per filter-value type
// so schema-required backends (redis, elasticsearch, opensearch, …)
// can declare each identifier with one fixed type:
//
//	author           — string-comparable
//	year             — number-comparable
//	published        — bool-comparable
//	n, a, b, c, d    — number-comparable (used in ordering / AND / OR)
//	tags             — string-list (IN)
//	years            — number-list (IN)
//	flags            — bool-list (IN)
//	title            — string-pattern (LIKE)
//	metadata['author'], metadata['a']['b'] — keyed access
//
// # Capability gaps
//
// A backend that genuinely doesn't support a shape (redis can't IN on
// numeric fields, for example) declares that case via [Options.Unsupported]. Each
// entry documents a real vendor capability gap; use sparingly.
package storetest
