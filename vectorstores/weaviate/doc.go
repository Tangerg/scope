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
// See https://weaviate.io/developers/weaviate for the full API
// surface.
package weaviate
