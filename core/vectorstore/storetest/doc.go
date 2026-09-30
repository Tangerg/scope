// Package storetest provides conformance suites for vector stores and filters.
//
// [Run] checks the exact capability set and validation before external I/O.
// Undeclared and missing capabilities both fail; a no-op Close is not cleanup.
// [VisitorConformance] checks filter compilation and [VisitorLifecycle] checks
// reuse after failure. Provider tests own exact wire assertions.
// [FilterConformance] runs isolated backend queries and compares selected IDs
// with exact expectations and [filter.Match]. Its callback must query the backend,
// not evaluate the predicate locally. Unsupported semantics need classified errors.
//
// # Field identifiers
//
// Schema-required backends assign one fixed type to each field:
//
//	author           - string
//	year             - number
//	published        - bool
//	n, a, b, c, d    - number
//	tags             - string list
//	years            - number list
//	flags            - bool list
//	title            - string pattern
//	profile['author'], profile['a']['b'] - keyed access
//	tags[0], profile['a'][0], items[0]['name'] - array element access
//
// # Compiler options
//
// [Options.InterpolatesKeyPaths] distinguishes keys inserted into query syntax
// from bound values. The former must reject unsafe names; the latter must
// preserve arbitrary keys.
//
// [Options.CompileText] checks numerals without lossy scalar conversions and
// that an array index never renders as the matching digit key.
// [Options.NumericDomainIsFloat64] permits equivalent float64 representations
// when that is the backend's numeric domain.
//
// [Options.Unsupported] names real capability gaps; each must fail explicitly.
package storetest
