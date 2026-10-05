// Package tidb implements Core vector-store capabilities using TiDB v8.4+
// native VECTOR columns and database/sql over the MySQL protocol. The host
// supplies the database handle and owns its lifecycle.
//
// Construction verifies the current storage schema even when InitializeSchema
// is false. IDs use VARBINARY(3072) NOT NULL as their sole full primary key;
// every uniqueness constraint must identify a document solely by that ID.
// Case, accents, trailing spaces, and embedded NUL bytes remain distinct. IDs
// over 3072 bytes and documents containing media are rejected before indexing
// I/O. Existing VARCHAR-ID or native-JSON-metadata tables must be rebuilt;
// the adapter neither migrates nor reads an obsolete schema.
//
// Metadata uses a single LONGBLOB NOT NULL column containing Core's JSON bytes.
// TiDB's binary JSON normalizes decimals through floating point and rejects
// valid JSON numbers outside that range. Core's codec retains encoded numbers,
// escaped strings, and the distinction between nil and empty metadata; Core
// filter.Match is the sole evaluator of selectors, operators, and failures.
//
// Filters scan metadata in bounded primary-key pages before embedding or
// deletion. A second pass projects matching IDs from the same repeatable-read
// transaction into native ranking or deletion. Deletes compare the original
// metadata bytes against current rows and commit all pages together. A
// concurrent metadata change or native write conflict fails the operation and
// rolls it back. The adapter returns native write conflicts without retrying.
//
// Searches use native cosine, L2, or negative-inner-product distance over the
// TiKV primary table, with exact ID ordering for distance ties, then apply
// MinScore. Schema initialization creates no ANN index and requires no TiFlash
// infrastructure. This chooses exact visible rows over approximate acceleration;
// filtering requires a metadata scan with bounded retained rows and wire IDs.
//
// Index embeds batches and upserts documents using JSON text for vector binding.
// Writes are not atomic across documents: a failure may leave earlier rows
// stored. Invalid stored metadata and backend errors remain explicit.
package tidb
