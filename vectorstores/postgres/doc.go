// Package postgres is the module entry for the pgvector and cockroachdb backends.
// Their public constructors select native resources; one private pgwire engine
// owns execution, transaction lifetime, and conversion to the Core contracts.
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
// Core filter.Match is the only metadata decision owner. Search and DeleteWhere
// read and validate the complete source in a serializable transaction, evaluate
// every predicate before embedding or mutation, and send only selected binary
// IDs to native SQL. No JSONB compiler, native error predicate, metadata index
// projection, or SQL-versus-Core arbitration participates. Core's exact numeric,
// nested path, Unicode LIKE, short-circuit, and type-error semantics apply,
// including array indices beyond native SQL int4 and NUL-bearing filter data.
//
// Search performs native distance ordering and TopK within the same snapshot;
// every returned document, vector, and finite distance is validated before
// MinScore. DistanceMetric selects a native query operator, rather than claiming
// an immutable resource metric. Native ANN indexes may approximate neighbors.
//
// DeleteWhere locks source rows until native removal and commit, so an observed
// metadata decision cannot delete an unlocked replacement. Predicate failure
// leaves all rows intact. DeleteIDs expresses direct identity intent separately.
// Index validates and prepares all batches and float32 vectors before its first
// upsert, then commits the entire request atomically. Serialization, native,
// embedding, and commit failures return errors; this engine adds no retry policy.
// The host owns the pgx pool and connection lifetime.
//
// These full-source reads favor exact Core semantics over SQL metadata indexes.
// Native integration tests create and remove isolated schemas. Run with
// -tags=integration and SCOPE_PGVECTOR_DSN or SCOPE_COCKROACHDB_DSN, selecting the
// corresponding subtest under TestLiveMetadataFilters and TestLiveCurrentOwners.
package postgres
