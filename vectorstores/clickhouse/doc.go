// Package clickhouse exposes ClickHouse vector similarity search
// through the Core vector-store capability interfaces. Documents live in a MergeTree table
// (id / content / metadata Map(String,String) / embedding
// Array(Float32)) reached through the official clickhouse-go v2
// driver.
//
// Automatic schema initialization requires the vector_similarity index type
// (HNSW-backed). Index creation errors fail construction. Hosts that provision
// the table themselves can set InitializeSchema to false.
//
// Distance metrics: [DistanceCosine] (uses `cosineDistance`) /
// [DistanceL2] (uses `L2Distance`). The store also wires the
// matching index distance parameter into the `vector_similarity`
// index definition.
//
// Metadata model. ClickHouse has no native JSON-path operator;
// metadata is a `Map(String, String)` and accessed via subscript
// (`metadata['key']`). The filter visitor reaches into it directly
// — numeric / ordering comparisons wrap the subscript in
// `toFloat64OrZero(...)` so range queries work.
//
// Insert path. Uses the typed batch API (`Conn.PrepareBatch` +
// `Batch.Append` + `Batch.Send`) — efficient for the bulk-insert
// shape ClickHouse expects.
//
// Delete uses a lightweight `DELETE FROM`, which waits until the rows are
// marked deleted before returning, so both delete paths keep the contract they
// advertise. `ALTER TABLE ... DELETE` cannot: it records a mutation and returns
// while the work still runs in the background. The lightweight statement needs
// a *MergeTree engine and the ALTER DELETE privilege, and it removes rows from
// query results without physically deleting them until a later merge.
//
// See https://clickhouse.com/docs/en/engines/table-engines/
// mergetree-family/annindexes for the official reference.
package clickhouse
