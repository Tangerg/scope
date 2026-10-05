// Package qdrant adapts existing Qdrant collections to Core Indexer, Searcher,
// IDDeleter and FilterDeleter capabilities. The host provisions the collection
// and owns the native SDK client, credentials, transport and client lifetime.
// Store owns no resource and does not implement Closer.
//
// NewStore binds a concrete collection name from native ListCollections, then
// reads its policy through GetCollectionInfo. Aliases, named or sparse vectors,
// multivectors, custom sharding and storage other than native FLOAT32 are not
// supported. The unnamed dense vector's native dimension and distance own policy;
// native default datatype means FLOAT32. Cosine, dot, Euclid and Manhattan are
// normalized solely through Core score functions. Keep the bound collection's
// lifecycle stable while Store is in use; replacing it requires a new Store.
//
// The current point payload has exactly two string fields: content and
// metadata_json. Core metadata.Map encodes and decodes the complete JSON value,
// including exact numbers, nested values, arbitrary keys and nil versus empty
// metadata. No business metadata is expanded into native payload fields. The
// native point ID alone owns document identity. IDs must be canonical decimal
// uint64 values or lowercase hyphenated UUIDs; other spellings are rejected
// before model or mutation I/O. Native dense vector output uses the current
// protobuf dense representation without a legacy vector-data fallback.
// Documents containing media are rejected before indexing I/O.
//
// Construction and every Search scroll the complete collection with payload and
// vectors, requiring every record to satisfy the current schema even outside a
// predicate or score threshold. Core filter.Match alone decides membership.
// Filtered queries constrain native metadata_json string membership in bounded
// groups. Both search paths use native ranking, merge raw native scores before
// TopK, and then apply Core normalization and MinScore. There is no inverse score
// formula or native relevance threshold hiding malformed results. The adapter
// supports semantic search only. Full scans require O(N) reads and identity and
// metadata tracking; query merging holds up to O(groups * TopK) hits.
//
// DeleteWhere completes Core selection, then deletes through native payload
// conditions on the selected metadata_json values. A point with a different
// current native payload value is excluded. These operations provide no shared
// read snapshot, revision CAS or per-record deletion counts: concurrent records
// with the same selected metadata may be deleted, and newly matching values
// outside selection may remain. Hosts needing a snapshot must coordinate writers.
// DeleteIDs validates all IDs and deduplicates before explicit identity deletion;
// empty input and unknown identities are idempotent.
//
// Index validates and encodes every record and completes every model batch and
// FLOAT32 validation before publishing one native upsert. Every mutation sets
// Wait and requires UpdateStatus_Completed. Acknowledged, WaitTimeout,
// ClockRejected and missing results remain explicit failures. Native request,
// transport and strict-mode limits remain provider errors; Store does not retry
// or claim transactional publication or a cross-request snapshot.
//
// Breaking replacement: remove DistanceMetric and its enum, Dimensions and
// InitializeSchema. Provision through the native host API, then supply Client,
// CollectionName, EmbeddingModel and DocumentBatcher. Rebuild expanded legacy
// payloads into the two current strings; there are no old-format reads, numeric
// adapters, schema creation, configuration arbitration or migration branches.
// Native payload indexes and operational policy remain host-owned.
//
// Default tests are offline. Integration tests require SCOPE_QDRANT_ADDR in
// host:port form, with optional SCOPE_QDRANT_API_KEY and SCOPE_QDRANT_TLS=true.
// They create isolated native collections, exercise all four distance metrics
// and the Core filter corpus, then delete those collections and close clients.
package qdrant
