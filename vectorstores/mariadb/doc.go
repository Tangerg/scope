// Package mariadb exposes MariaDB's native VECTOR column type
// through the Core vector-store capability interfaces. Documents live in a regular MariaDB table
// (id / content / metadata JSON / embedding VECTOR) reached through
// `database/sql` + the go-sql-driver/mysql driver.
//
// Requirements: MariaDB 11.6+ (vector support landed in 11.6 GA;
// the VECTOR INDEX HNSW backing only became stable in 11.7).
//
// Distance metrics: [DistanceCosine] (uses `vec_distance_cosine`) /
// [DistanceEuclidean] (uses `vec_distance_euclidean`). A MariaDB vector index
// is built for one distance function and serves only queries that name that
// same function, so [StoreConfig.InitializeSchema] states the configured metric
// as the index's DISTANCE option. A table provisioned elsewhere must declare
// the matching DISTANCE, or searches fall back to a full table scan and still
// return correct rows — a degradation nothing surfaces.
//
// Vector binding. MariaDB accepts vectors through the `VEC_FromText`
// function — the store renders `[v1,v2,...]` as a literal and lets
// MariaDB parse it. Typed binary binding isn't exposed by the Go
// driver yet, but the textual form is fully supported.
//
// Filter visitor reaches into the JSON metadata column with
// `JSON_VALUE(metadata, '$.k')`, wrapping numeric comparisons in
// `CAST(... AS DECIMAL(65,30))` so range queries don't fall back to
// lexicographic ordering.
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
// See https://mariadb.com/kb/en/vector-overview/ for the official
// reference.
package mariadb
