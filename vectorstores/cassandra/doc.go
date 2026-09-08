// Package cassandra exposes Apache Cassandra 5.0+ vector support
// through the Core vector-store capability interfaces. Documents live in a regular CQL table with
// a `vector<float, N>` column; filterable metadata keys must be declared as
// typed columns (Cassandra has no JSON-path operator), each indexed
// via a Storage Attached Index (SAI).
//
// Metadata model. A document's metadata of record is the JSON in
// [StoreConfig.MetadataColumn], which carries no SAI index. The declared typed
// columns are the filterable projection of that record. CQL reaches a metadata
// key only as a declared column, so writing only those columns dropped every
// other key with no error and no way to get it back, and reading them back
// returned a document without those keys. Reading the record instead makes the
// round trip exact and keeps undeclared keys; declaring a column is what makes
// a key filterable, not what makes it stored.
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
// Scores. Apache Cassandra documents the similarity_cosine,
// similarity_dot_product and similarity_euclidean signatures but not the
// range of what they return, so the store takes the value as a relevance
// score already on Core's scale and clamps it. That is an assumption about
// an undocumented property rather than a mapping: if a server reports a
// value outside the range, the clamp keeps Cassandra's ordering only below
// the bound. similarity_dot_product also assumes L2-normalized vectors —
// Cassandra does not normalize for it — so a non-normalized embedding makes
// the value meaningless before it ever reaches a score.
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
// via [StoreConfig.MetadataColumns] entries with their CQL type (text / int /
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
