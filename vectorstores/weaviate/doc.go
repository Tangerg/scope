// Package weaviate exposes Weaviate through the Core vector-store capability interfaces.
// Documents are stored as objects in a Weaviate class (`{id,
// vector, properties}`). Semantic retrieval runs `nearVector`; hybrid
// retrieval combines the supplied vector with lexical evidence from `content`
// through relative-score fusion. [StoreConfig.HybridAlpha] optionally controls
// vector weight.
//
// Requirements: a reachable Weaviate v5 server (self-hosted or
// Weaviate Cloud Services). The store uses the official
// weaviate-go-client/v5.
//
// Vector similarity functions: cosine / dot / l2-squared / hamming
// / manhattan. The chosen value is bound to the class's vector
// index config at creation time.
//
// Schema. Weaviate is strongly typed — properties participating in
// filters must be declared at class-creation time. [StoreConfig]
// enumerates these properties so the store can issue a CREATE
// CLASS when needed.
//
// Filter visitor produces Weaviate's `where` filter operator tree
// — `{"operator": "Equal", "path": ["author"], "valueText": "..."}`,
// `{"operator": "And", "operands": [...]}`, `{"operator":
// "GreaterThan", "valueNumber": 100}`. The result feeds the
// `WithWhere` builder on the GraphQL Get call.
//
// Batch acknowledgment. Weaviate answers a batch whose objects individually
// failed with a successful call, so Index requires one SUCCESS result per
// object it sent. A rejected object returns an error while the objects accepted
// in the same batch remain stored.
//
// Metadata filtering needs declared properties. Weaviate classes are typed and
// a where filter may only name a declared property, so
// [StoreConfig.MetadataProperties] enumerates the keys filters may select on.
// Each becomes a class property under InitializeSchema and is written alongside
// the document; the complete metadata map is also stored as JSON so every key
// round-trips losslessly whether or not it is filterable. A filter naming an
// undeclared key is refused, because a path with no matching field is not a
// narrower query but one the server cannot answer.
//
// A declared text property pins field tokenization, which "treats the entire
// value of the property as a single token" and "preserves both case and
// symbols". Weaviate's default word tokenization splits on non-alphanumeric
// characters and lowercases each token, which would make equality a token
// match rather than the whole-value, case-sensitive comparison a filter asks
// for. The content property keeps word tokenization, which is what hybrid
// search needs.
//
// A nested metadata key cannot be filtered: it would need an object property
// with declared nestedProperties, and dotted-path filtering on those leaves is
// a Weaviate v1.38 preview feature.
//
// Filtered deletion repeats. One batch delete removes at most
// QUERY_MAXIMUM_RESULTS objects — the response calls Successful the count "in
// this round" — and Weaviate's guidance for a filter that matches more is to
// re-run the query, so DeleteWhere does until the round deletes everything it
// matched. Objects that could not be deleted are reported in Failed rather
// than as a call error, so that count is checked too; a round that matches
// more than it deletes while deleting nothing is refused instead of repeated,
// since it cannot progress.
//
// See https://weaviate.io/developers/weaviate for the full API
// surface.
package weaviate
