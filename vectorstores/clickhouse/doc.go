// Package clickhouse exposes ClickHouse vector similarity search
// through the Core vector-store capability interfaces. Documents live in a MergeTree table
// (id / content / metadata Map(String,String) / embedding
// Array(Float32)) reached through the official clickhouse-go v2
// driver.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
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
// Metadata model. Metadata is a `Map(String, String)` accessed via subscript
// (`metadata['key']`), and each value is stored as its JSON text.
// [metadata.Map] is a map of JSON values, so the JSON text is the exact value
// and this column holds it verbatim: a document reads back with the types it
// was written with, and a nil value stays distinguishable from an empty
// string. A string value therefore carries its quotes, which is why a filter
// binds the JSON encoding of a literal rather than its bare text, and why LIKE
// matches the pattern against the quoted form — [filter.OpLike] matches the
// whole value rather than a substring of it, so quoting the pattern keeps the
// match anchored where the operator says it is.
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
// Keys the AST reads as nil. The filter AST reads both a key that is absent
// and a key whose value is null as nil, so a total leaf answers for both. A
// Map(String, String) subscript answers an absent key with the empty string,
// so a comparison could not tell "not there" from "empty", and the old numeric
// conversion turned anything it could not parse into zero — which let a range
// match a row that has no such key. Each comparison, IN and LIKE leaf now asks
// mapContains and tests the stored null text, carrying the truth value the AST
// assigns nil, so the leaf is total and negation composes. IS NULL asks the
// same pair of questions.
//
// Numeric comparisons convert with toDecimal128OrNull rather than a float:
// Float64's 53-bit mantissa cannot hold every int64, so an id past 2^53 would
// compare equal to its neighbor. A present but non-numeric value becomes NULL
// and drops the row; the AST reports that case as an error, so there is no
// decided answer for the server to disagree with.
//
// See https://clickhouse.com/docs/en/engines/table-engines/
// mergetree-family/annindexes for the official reference.
package clickhouse
