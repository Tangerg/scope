// Package cockroachdb provides a native CockroachDB vector-store adapter over
// pgwire. It uses CockroachDB's built-in VECTOR type, pgvector-compatible
// distance operators, and native VECTOR INDEX declaration.
// Documents containing media are rejected before embedding or writes; this
// store persists text and metadata only. Index batches commit independently,
// so an error can leave earlier batches stored.
//
// Requirements: CockroachDB v25.4 or later is recommended for generally
// available vector indexing. Schema initialization is explicit through
// StoreConfig.InitializeSchema.
//
// Metadata filters use the shared PostgreSQL-family JSONB compiler and execution
// boundary. Scalar types and exact JSON numbers stay distinct; missing and null
// values follow Core's boolean truth, including negation. Ordering and LIKE
// type errors are checked before query embedding or deletion, in the same
// serializable snapshot as execution. Database conflicts return errors.
package cockroachdb
