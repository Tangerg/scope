// Package tidb exposes TiDB's native VECTOR column type
// through the Core vector-store capability interfaces. Documents live in a regular TiDB table
// (id / content / metadata JSON / embedding VECTOR) reached over
// the MySQL wire protocol via `database/sql` +
// go-sql-driver/mysql.
//
// Requirements: TiDB 8.4+ (vector type GA) — TiDB Serverless
// supports it on every recent release. The HNSW vector index needs
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
// comparisons in `CAST(... AS DOUBLE)`.
//
// Partial writes. Index prepares one upsert and runs it per document without
// wrapping the batch in a transaction, so a failure leaves the rows already
// written in place. The returned error names the id that failed, and repeating
// the call is safe because the statement is idempotent per row.
//
// See https://docs.pingcap.com/tidb/stable/vector-search-overview/
// for the official reference.
package tidb
