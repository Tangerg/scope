// Package cassandra exposes Apache Cassandra 5.0+ vector support
// through the Core vector-store capability interfaces. Documents live in a regular CQL table with
// a `vector<float, N>` column; metadata keys must be declared as
// typed columns (Cassandra has no JSON-path operator), each indexed
// via a Storage Attached Index (SAI).
//
// Requirements: Apache Cassandra 5.0+ or compatible (DataStax Astra
// DB / DataStax Enterprise). Vector + SAI both arrived together in
// 5.0. The store uses gocql v1.x.
//
// Similarity functions — recorded in the SAI index definition at
// creation time:
//
//   - [SimilarityCosine]      — cosine similarity (default)
//   - [SimilarityDotProduct]  — inner product
//   - [SimilarityEuclidean]   — Euclidean distance
//
// Vector binding caveat. gocql v1.x has no first-class
// `vector<float, N>` codec, so the store inlines vectors as CQL
// literals (`[v1, v2, ...]`) into the SQL. Cassandra accepts that
// form for both INSERT and ORDER BY ANN OF. The other parameters
// flow through normal `?` placeholders.
//
// Filter constraints. CQL on regular columns doesn't support `OR`
// or standalone `NOT`; the visitor rejects them with a clear error.
// `IN` is fine and binds as a typed slice. Every filterable
// metadata key must exist as a typed column on the table, declared
// via [MetadataColumn] entries with their CQL type (text / int /
// boolean / double / …).
//
// Filter-based DELETE. Cassandra forbids deleting by a non-PK
// predicate. The store works around it by SELECT-ing matching ids
// first then issuing per-row DELETEs.
//
// Partial writes. Neither Index nor DeleteWhere is atomic: both walk their rows
// one statement at a time, so a failure leaves the statements already executed
// applied. The returned error names the id that failed, and the operation is
// safe to repeat because both statements are idempotent per row.
//
// Filter limits come from CQL, not from this store: a WHERE clause supports
// neither OR nor a standalone NOT, has no IS NULL and no LIKE on a metadata
// column, and reaches a metadata key only as a declared column — so an indexed
// or nested key cannot be filtered either.
//
// See https://cassandra.apache.org/doc/latest/cassandra/vector-search/
// for the official reference.
package cassandra
