// Package postgres owns the pgwire vector store shared by the pgvector and
// cockroachdb backends. Store owns execution, transaction lifetime, and
// conversion to the Core contracts; each backend's NewStore provisions its
// native schema and returns that one Store.
//
// The strict current table has exactly four non-null columns with no defaults:
// binary id as the sole primary key, text content, binary metadata, and a fixed
// width native vector embedding. Binary identity preserves Core ID bytes without
// SQL collation or NUL limitations. Binary metadata contains the complete Core
// JSON encoding; null, an empty object, exact numeric spelling, arbitrary keys,
// and large exponents survive unchanged. PostgreSQL uses BYTEA; CockroachDB uses
// BYTES. Content uses native text and cannot contain NUL; Index rejects it before
// I/O. Metadata may contain encoded NUL values.
//
// NewStore validates the deployed column types, constraints, and vector width.
// InitializeSchema creates the current schema when requested, but never alters
// an existing table. Dimensions seeds only a new column. Every operation reads
// the native schema again; no Scope width override advances that fact.
// Existing text-ID/JSONB tables are rejected. Recreate the table and reindex from
// authoritative documents; there are no compatibility reads or migrations.
//
// Ordinary Query ranks the supplied vector with native distance and TopK,
// without enumerating source rows. Opaque binary metadata has no equivalent
// native predicate here: filters return vectorstore.ErrUnsupportedFilter and
// FilterDeleter is deliberately not implemented. DeleteIDs is explicit.
// Host-authorized Scan accepts explicit record, payload-byte, and time budgets.
// It checks completeness in one read-only transaction before loading records;
// a budget failure returns no partial matches or invented empty result.
//
// The table carries a named CHECK constraint derived from the bound space ID.
// Opening an existing table with a different ID is rejected; a missing marker
// requires rebuilding the table, not labeling its unknown vectors. Reusing
// an ID for different preprocessing or a different model violates that contract.
// Validate width and float32 conversion against the current native schema before
// publication. Upsert batches use one transaction and no model I/O. Loss of a
// commit acknowledgment leaves all submitted IDs unconfirmed. The Host owns
// provisioning, the pgx pool, and any decision to reconcile an unknown outcome.
package postgres
