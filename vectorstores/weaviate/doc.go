// Package weaviate stores vectors and queries a host-provisioned Weaviate
// collection using the official v5 client. The host owns its credentials,
// transport and class lifecycle. NewStore and each operation inspect the
// current class: its description must equal SpaceID.StorageKey(), vectorizer
// must be none, and its one unnamed dense index must have a supported native
// distance. The class cannot use multi-tenancy. Provision content (searchable
// word text), metadata (field text) and metadataTerms (filterable field text[]).
//
// Upsert accepts prepared vectors, validates all document UUIDs and finite
// FLOAT32 coordinates before submitting a batch, and never lists existing
// objects to infer a dimension. Weaviate enforces its stored native width.
// Partial application is visible through vectorstore.WriteError, including
// confirmed, rejected and unconfirmed identities. A failed request does not
// imply that no objects were stored. A new index may be asynchronously visible.
//
// Canonical Core metadata remains one lossless JSON string. The same object
// write carries an exact, derived keyword list for native scalar top-level
// string equality. Query pushes this predicate into a single native request.
// Unsupported filters fail without source enumeration. GraphQL nearVector
// preserves native distance ordering; hybrid uses one native relative-score
// fusion with both prepared coordinates and explicit lexical text. HybridAlpha
// preserves the native default when nil. Bad hit shape, invalid score, current
// metadata mismatch or a vector of the wrong width invalidate the whole reply.
//
// DeleteIDs addresses explicit canonical UUIDs. There is no predicate deletion
// or client-side Scan capability. The v5 data client does not expose a bounded
// streaming source reader and the adapter does not synthesize a full pass.
// Coordinate schema changes with writers: the schema read and subsequent
// object operation do not form a transaction.
//
// This adapter requires the server's GraphQL API for queries:
// https://docs.weaviate.io/weaviate/client-libraries/go.
// Integration tests require SCOPE_WEAVIATE_URL on an isolated host and create
// and remove disposable collections there.
package weaviate
