// Package oracle exposes Oracle 23ai's native VECTOR column type
// through the Core vector-store capability interfaces. Documents live in a regular Oracle table
// (id / content / metadata JSON / embedding VECTOR) reached through
// `database/sql` + the sijms/go-ora driver.
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
