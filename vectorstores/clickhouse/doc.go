// Package clickhouse implements Core vector-store capabilities with native
// ClickHouse distances and transaction snapshots through clickhouse-go v2.
// The host supplies a database/sql handle using the native TCP protocol and
// owns its lifecycle. An omitted database is resolved once at construction.
//
// The server must enable native experimental transactions and configure Keeper.
// Construction probes BEGIN/ROLLBACK and rejects unsupported servers. Every
// adapter INSERT and unfiltered read uses implicit_transaction; insertion is
// synchronous. Filtered reads and both deletions lease a TCP session for explicit
// native transactions.
// database/sql BeginTx alone only controls the driver's buffered insert batch.
// Concurrent external readers and writers must also use native transactions:
// nontransactional writes bypass snapshots and nontransactional reads can see
// uncommitted mutations. Existing external producers and readers must be updated.
// Native transaction setup is documented in the upstream integration fixture:
// https://github.com/ClickHouse/ClickHouse/blob/v26.8.17.4-lts/tests/config/config.d/transactions.xml
//
// Documents use an unpartitioned ReplacingMergeTree without version arguments,
// ordered only by their String ID. FINAL supplies its current-record view;
// native insertion order is the sole replacement authority. Content and metadata
// use String, embeddings use Array(Float32). vec_len fixes the dimension and
// vec_finite rejects non-finite stored values. Columns cannot generate values.
// Dimensions only configures schema creation. Construction validates the current
// schema even when InitializeSchema is false. Tables using Map metadata or older
// constraints must be rebuilt and reindexed; there is no alternate read format.
// Documents containing media are rejected before indexing I/O.
//
// Metadata contains Core's complete encoded JSON, preserving null versus an
// empty object, encoded numbers, escaped strings, and nested arrays and objects.
// Core filter.Match is the sole owner of selector, operator, and failure semantics.
// Filters validate every current document in bounded ID pages before embedding
// or deleting. Both passes use the same native snapshot. No SQL filter compiler
// or native numeric conversion can decide metadata membership.
//
// Search orders exact native cosineDistance or L2Distance by distance and exact
// ID bytes. Filtered search scans that ordered snapshot without an early LIMIT,
// retains only TopK matches, and applies Core's relevance conversion and threshold.
// Initialization creates no approximate index.
//
// Deletion uses synchronous lightweight DELETE, removing all snapshot-visible
// versions of selected IDs. All pages share one native commit or rollback; a
// later failure cannot publish earlier pages. Concurrent transactional inserts
// remain outside that snapshot. Rows become unqueryable on success and their
// physical removal follows ClickHouse's later merges. Required transaction,
// deletion, and overflow settings are pinned by the adapter. SDK query options
// inherited from caller contexts are cleared; context cancellation and ordinary
// values still propagate. Backend and cleanup failures remain explicit;
// uncertain sessions are discarded without retrying.
package clickhouse
