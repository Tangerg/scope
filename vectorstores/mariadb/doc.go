// Package mariadb implements Core vector-store capabilities using MariaDB's
// native VECTOR columns, JSON metadata, and database/sql. The host supplies a
// database handle, typically using github.com/go-sql-driver/mysql, and owns its
// lifecycle. Community Server 11.7+ or Enterprise Server 11.4.5-3+ is required.
//
// Construction verifies the current identity schema even when
// StoreConfig.InitializeSchema is false. Documents use a VARBINARY(3072) NOT NULL
// primary key so case, accents, trailing spaces, and embedded NUL bytes cannot
// alias another ID. The table must use InnoDB, and every uniqueness constraint
// must identify a document solely by that ID. Existing VARCHAR-ID tables must
// be rebuilt; the adapter does not migrate or read an obsolete schema. IDs over
// 3072 bytes and documents containing media are rejected before indexing I/O.
//
// Metadata JSON is the sole stored filter representation. Core filter.Match
// is the sole evaluator, including exact numbers, typed selectors, null/missing
// truth, immediate array membership, LIKE, and error short circuits. A filter
// scans metadata in bounded pages before embedding or deletion, then projects
// matching IDs from that same transaction into native ranking or deletion.
// Searches retain a repeatable-read snapshot; deletes use serializable reads
// and commit all matching deletions together. This requires a metadata scan,
// with bounded retained IDs rather than a table-sized client-side result set.
//
// DistanceMetric selects the native distance function. Searches rank primary
// table rows directly, with exact ID ordering for distance ties, then apply
// MinScore. The native ANN index can omit existing rows after replacement and
// is excluded from queries even when a host provisioned one externally. Schema
// initialization creates a VECTOR column without an ANN index; this trades
// approximate-search acceleration for consistent visibility and filter truth.
//
// Every returned row is decoded and validated by Core before MinScore; a low
// score never hides an invalid native document or malformed metadata.
//
// Index validates the whole request, embeds batches, and upserts each document.
// Writes are not atomic across documents: a failure may leave earlier rows
// stored. Vector values are bound as JSON text through VEC_FromText. Nil and
// empty metadata retain their distinct JSON encodings.
package mariadb
