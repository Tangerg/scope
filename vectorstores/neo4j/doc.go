// Package neo4j exposes the official neo4j-go-driver v5
// through the Core vector-store capability interfaces. Documents become nodes labeled `:Document`
// (or whatever [StoreConfig.Label] picks) — metadata keys are
// stored as flat properties named `metadata.<key>`, the embedding
// rides on the configured property, and the id has a uniqueness
// constraint.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Requirements: Neo4j 5.13+ for `CREATE VECTOR INDEX` and the
// `db.index.vector.queryNodes` procedure. Earlier 5.x releases ship
// the procedure under a different signature; the store hard-codes
// the 5.13+ shape.
//
// Similarity functions: [SimilarityCosine] / [SimilarityEuclidean].
// Both are mapped to a [0, 1] similarity score by Neo4j itself.
//
// Indexing — the store creates two things under
// [StoreConfig.InitializeSchema] = true:
//
//   - a uniqueness constraint on the id property
//   - a `VECTOR INDEX` carrying dimensions + similarity function
//
// Search calls `CALL db.index.vector.queryNodes($index, $k, $vec)
// YIELD node, score WHERE score >= $threshold AND <filter>`. The
// filter visitor produces a Cypher predicate plus a `$pN`-keyed
// parameter map (Cypher uses named parameters).
//
// TopK is a candidate budget, not a result count. The procedure takes no
// predicate, so it returns the k nearest nodes first and the metadata filter
// then removes some of them: a selective filter yields fewer than TopK results,
// possibly none, however many matching nodes the graph holds. Raise TopK to
// widen the pool a filter draws from.
//
// LIKE maps onto Cypher's `=~` (regex). Note that NOT in Cypher
// must precede an expression — the visitor emits `NOT (<expr>)`.
//
// See https://neo4j.com/docs/cypher-manual/current/indexes-for-vector-search/
// for index syntax and the vector-search reference.
// Metadata numbers use signed 64-bit integers where exact, otherwise doubles
// whose decimal JSON value round-trips without loss. Unrepresentable numbers
// are rejected at the payload boundary.
package neo4j
