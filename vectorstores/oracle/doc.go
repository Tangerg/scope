// Package oracle implements Core vector-store capabilities using Oracle 23ai+
// native VECTOR columns and database/sql with go-ora v2. The host supplies the
// handle and owns its lifecycle; the adapter uses the driver's native CLOB and
// BLOB bindings. An omitted schema is resolved once at construction and every
// operation uses the resulting qualified table name.
//
// Construction verifies the current storage schema even when InitializeSchema
// is false. IDs use RAW(2000) NOT NULL as their sole primary key; no uniqueness
// constraint or index may introduce another document identity. IDs retain exact
// UTF-8 bytes independently of character collation. IDs over 2000 bytes and
// documents containing media are rejected before indexing I/O. Existing
// VARCHAR2-ID or native-JSON-metadata tables must be rebuilt; the adapter neither
// migrates nor reads an obsolete schema.
//
// Metadata uses one BLOB NOT NULL column containing Core's JSON bytes. Native
// JSON can change numeric precision, range, and type. Core's codec retains
// encoded numbers, escaped strings, and nil versus empty metadata. Core
// filter.Match is the sole evaluator of selectors, operators, and failures.
//
// Filters validate all metadata in bounded primary-key pages before embedding
// or deletion. Filtered search uses one native serializable snapshot for both
// passes and ranking. Filtered deletion takes an exclusive native table lock
// before evaluation and retains it until commit or rollback. Lock acquisition
// uses NOWAIT: competing writes cause a native busy error without queuing or
// retrying the deletion. While the lock is held, ordinary queries remain
// available and other writes must wait or fail according to their native lock
// policy. All deletion pages are atomic.
// A backend or later page failure rolls back the deletion without retries.
//
// Searches use exact VECTOR_DISTANCE with cosine, Euclidean, or negative inner
// product distance, ordering ties by exact ID bytes before applying MinScore.
// Initialization creates no approximate vector index. Filtering scans metadata
// with bounded retained pages and wire IDs. TO_VECTOR derives dimensions from
// its input; the native column owns the stored dimension constraint. Dimensions
// configures only schema creation.
//
// Index embeds batches and upserts through MERGE. Writes are not atomic across
// documents: a failure may leave earlier rows stored. Invalid stored metadata
// and backend errors remain explicit.
package oracle
