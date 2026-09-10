// Package oracle exposes Oracle 23ai's native VECTOR column type
// through the Core vector-store capability interfaces. Documents live in a regular Oracle table
// (id / content / metadata JSON / embedding VECTOR) reached through
// `database/sql` + the sijms/go-ora driver.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Requirements: Oracle Database 23ai (the AI release). VECTOR is
// a first-class column type in 23ai, with `VECTOR_DISTANCE()` and
// `TO_VECTOR()` built-ins.
//
// Distance metrics — three of Oracle's standard variants are
// exposed:
//
//   - [DistanceCosine]    — cosine distance
//   - [DistanceEuclidean] — L2 distance
//   - [DistanceDot]       — dot product (Oracle returns the raw IP;
//     the store maps it into [0, 1] via (1 + ip) / 2)
//
// Searches are exact, and deliberately so. Oracle separates exact from
// approximate purely by syntax — `FETCH FIRST n ROWS ONLY` compares the query
// vector against every row, `FETCH APPROX FIRST n ROWS ONLY` permits a vector
// index — and this store issues the former, so every result is a true nearest
// neighbor. [StoreConfig.InitializeSchema] correspondingly creates the table
// and no vector index: an HNSW index needs the CDB-level `vector_memory_size`
// raised from its default of 0 and is unavailable on RAC, so provisioning one
// would fail the bootstrap on ordinary deployments rather than accelerate it.
//
// An operator who wants approximate search owns both halves. The index's
// DISTANCE must be the metric configured here, because "if you use a different
// distance function than the one used to create the index, an exact match is
// triggered because you cannot use the index in this case" — Oracle defaults
// both the index and `VECTOR_DISTANCE()` to COSINE, which is also
// [DefaultDistanceMetric]. Note that the index still will not be used until the
// query asks for it, and this store's query does not; Oracle reports none of
// this, silently falling back to a full scan.
//
// Vector binding. The store renders `[v1,v2,...]` as text and wraps
// each call in `TO_VECTOR(:1, <dim>, FLOAT32)`. Oracle's positional
// `:N` placeholders mean the filter visitor's placeholders are
// renumbered to start after the query-vector's `:1` slot.
//
// Filter visitor reaches metadata with `json_value(metadata,
// '$.key' RETURNING NUMBER)` for numeric / ordering comparisons so
// the predicate runs against typed numbers, not text. String
// comparisons drop the RETURNING clause.
//
// Partial writes. Index prepares one MERGE and runs it per document without
// wrapping the batch in a transaction, so a failure leaves the rows already
// written in place. The returned error names the id that failed, and repeating
// the call is safe because the statement is idempotent per row.
//
// See https://docs.oracle.com/en/database/oracle/oracle-database/23/
// vecse/index.html for the official reference.
package oracle
