// Package clickhouse stores and ranks precomputed vectors with clickhouse-go v2
// over native TCP. The host supplies and owns the database/sql pool. The physical
// table is Space.StorageKey() + "_" + TableName in the bound database. Even
// equal-width spaces with the same logical TableName use different tables and
// cannot replace each other's IDs. Reindex legacy unprefixed tables explicitly.
//
// Construction probes native transactions and reads the real four-column
// table schema: unpartitioned ReplacingMergeTree ordered only by ID, three
// String columns and Array(Float32), vec_len and vec_finite. The native length
// constraint owns the width. A positive CreateDimensions provisions a missing
// table, and an existing table must already declare that width. The host must not replace the table
// while a Store is in use. Metadata is one exact Core JSON string, including
// null, precise numbers and escaped values. Media documents are unsupported.
//
// Upsert preflights the complete batch before obtaining a driver insert buffer.
// Commit sends one synchronous native INSERT with implicit_transaction. A
// rejected input cannot publish earlier rows. Before send all IDs remain
// unattempted; a lost send acknowledgment leaves all of them unconfirmed.
// Native FINAL reads choose the current version.
//
// Query runs one native ORDER BY distance, id LIMIT statement, using the
// selected cosine or L2 distance function, and applies the relevance threshold
// to validated rows. It does not create an approximate index. An opaque filter
// returns ErrUnsupportedFilter. Scan is an explicit alternative: its budget
// counts every source ID, text and raw metadata byte, including nonmatches.
// Each native page contains up to 512 rows; transport framing is separate.
// Scan charges records before decoding. Failure returns no partial rows.
//
// DeleteMatching reads and validates the complete bounded snapshot before the
// first delete. Its ID chunks remain inside that same native transaction.
// DeleteIDs acts directly on explicit identity. Native COMMIT is the publication
// boundary; lost acknowledgment makes the selected IDs unconfirmed. Synchronous
// lightweight deletion hides rows upon success; physical removal follows merges.
// Cancellation releases native sessions using a separately bounded rollback.
//
// Native experimental transactions require Keeper. All concurrent readers and
// writers must use transactions to participate in these snapshot guarantees.
// SDK query settings that could bypass transaction or visibility semantics are
// replaced. Ordinary context values and cancellation propagate. For live tests
// set SCOPE_CLICKHOUSE_DSN to an isolated suitable server. See upstream setup:
// https://github.com/ClickHouse/ClickHouse/blob/v26.8.17.4-lts/tests/config/config.d/transactions.xml.
package clickhouse
