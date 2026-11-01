// Package chroma adapts a Host-owned Chroma collection to vectorstore.Upserter,
// vectorstore.Querier and vectorstore.IDDeleter. It does not encode text, open
// collections, create schemas, install metadata or close the borrowed SDK handle.
//
// The Host exclusively provisions an encoder pairing in collection metadata
// under SpaceMetadataKey and establishes the native dimension before handing
// the adapter a freshly resolved collection. Chroma reports dimension zero
// until the first vector is inserted. NewStore rejects an unbound width, a
// missing or mismatching space marker, or an unsupported native metric.
// Collection.Metadata, Dimension and Schema describe the SDK handle; they
// are not a live schema lock. No administrative changes are permitted behind
// the bound handle. Reopen after administrative changes. Equal dimensions alone
// do not make embedding spaces compatible.
//
// A batch is fully validated and narrowed to finite float32 coordinates before
// its one native Upsert. An acknowledgment does not guarantee search visibility;
// a failed attempted RPC reports its IDs as unconfirmed without retrying. A
// document retains its original ID, text and exact metadata JSON in a single
// scope_metadata string. Native media storage is unsupported.
//
// Query is native semantic ANN only. It never enumerates the source and never
// evaluates an opaque metadata predicate after truncating to top-K. A predicate
// returns vectorstore.ErrUnsupportedFilter before either Get or Query. Returned
// records, dimensions and native scores are fully validated before MinScore is
// applied. Native distances rank rows even when normalized Core scores tie.
//
// Scan explicitly authorizes Get pagination with finite record, observed-byte
// and time budgets. It evaluates Core metadata semantics over every observed
// source record and returns no partial result if a limit, validation or transport
// error occurs. For a stable collection this enumerates all records; offset
// pagination is not transactionally consistent with concurrent writes. The Host
// must quiesce writers for repeatable membership. Chroma cannot atomically
// delete by an arbitrary Core predicate; Store intentionally does not implement
// vectorstore.FilterDeleter.
package chroma
