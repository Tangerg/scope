// Package tidb exposes TiDB's native VECTOR column type
// through the Core vector-store capability interfaces. Documents live in a regular TiDB table
// (id / content / metadata JSON / embedding VECTOR) reached over
// the MySQL wire protocol via `database/sql` +
// go-sql-driver/mysql.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Requirements: TiDB v8.4.0+, which is where PingCAP sets the floor for
// self-managed and Dedicated clusters while recommending v8.5.0 or later. The
// vector data type still carries a beta notice, so it "might be changed without
// prior notice". The HNSW vector index needs
// the function-expression form
// `((VEC_<metric>_DISTANCE(embedding))) USING HNSW` and is only
// available on TiKV-backed columnar storage in some deployments;
// the store creates it under [StoreConfig.InitializeSchema] = true
// and propagates any backend error so callers can react.
//
// Distance metrics — they map to TiDB's built-in functions:
//
//   - [DistanceCosine]     → `VEC_COSINE_DISTANCE`
//   - [DistanceL2]         → `VEC_L2_DISTANCE`
//   - [DistanceNegativeIP] → `VEC_NEGATIVE_INNER_PRODUCT`
//
// Vector binding. TiDB accepts `'[v1,v2,...]'` text literals
// directly — the store renders them and binds as a regular `?`
// parameter, so no special vector codec is needed.
//
// Filter visitor reaches into the JSON metadata column with
// `JSON_VALUE(metadata, '$.k')`, wrapping numeric / ordering
// comparisons in `CAST(... AS DECIMAL(65,30))`.
//
// Partial writes. Index prepares one upsert and runs it per document without
// wrapping the batch in a transaction, so a failure leaves the rows already
// written in place. The returned error names the id that failed, and repeating
// the call is safe because the statement is idempotent per row.
//
// Numeric comparisons cast to DECIMAL, not DOUBLE. DOUBLE is an approximate
// type whose 53-bit mantissa cannot hold every int64, so an id or timestamp
// past 2^53 would compare equal to its neighbor and match the wrong row.
// DECIMAL stores exact values up to the documented 65 digits, which covers
// every integer the filter AST can carry — and the AST compares as a rational
// precisely so an integer is never rounded to a float's precision.
//
// See https://docs.pingcap.com/tidb/stable/vector-search-overview/
// for the official reference.
package tidb
