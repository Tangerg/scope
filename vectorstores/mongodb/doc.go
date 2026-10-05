// Package mongodb adapts an existing MongoDB Vector Search collection to Core
// Indexer, Searcher, IDDeleter and FilterDeleter. The host owns the official v2
// driver client, authentication, transport, write concern, collection and index
// provisioning, and lifetime. Store owns no resource and does not implement Closer.
//
// Construction reads the named native index through $listSearchIndexes. It must
// be a READY, queryable vectorSearch index with one embedding vector, a positive
// native dimension, a supported native cosine, euclidean or dotProduct policy,
// and an _id filter path. BUILDING remains invalid even if an older definition
// is queryable. Store projects only the native dimension; similarity and score
// normalization remain entirely native. Keep the bound collection and index
// policy stable during Store's lifetime; reconstruct Store after replacement.
// A deployment without Vector Search or index-list read privileges fails at
// construction. Store does not create indexes, probe models or arbitrate policy.
//
// The current BSON document has exactly _id, content, metadata_json and
// embedding. Identity is the native string _id alone. Content and metadata_json
// are strings; embedding is an array of finite FLOAT32 values encoded as BSON
// doubles. Core metadata.Map owns the complete JSON encoding, preserving nil,
// empty objects, exact numbers, nested values and arbitrary user keys. Business
// metadata never becomes BSON fields or an independent numeric representation.
// Media documents are rejected before model or mutation IO.
//
// Construction and every Search enumerate the complete collection, validating
// all current records even outside a predicate or MinScore. Core filter.Match
// alone owns membership. Filtered $vectorSearch queries use bounded selected
// identity groups and verify each returned identity and current metadata. Both
// paths use native ranking and native [0,1] scores, merge before TopK, and apply
// MinScore after strict hit validation. Scores are never clamped or recomputed.
// Search supports semantic mode only. Enumeration costs O(N) source reads and
// identity tracking; query merging holds up to O(groups * TopK) hits.
//
// NumCandidates is an ANN recall floor bounded by the native 10000 ceiling and
// raised to cover TopK. TopK beyond that ceiling fails before native or model IO.
// Vector Search index visibility remains asynchronous. An acknowledged write
// does not imply immediate query visibility; Store adds no retry or polling.
//
// Index encodes every document and completes every model batch and vector check
// before one BulkWrite. The native driver owns request batching and limits.
// MatchedCount plus UpsertedCount must cover every replacement, and w:0 is an
// explicit failure. This is not transactional publication: native IO can apply
// a prefix before failing. Native _id selectors use binary collation for writes
// and deletions rather than inheriting a case-insensitive collection collation.
//
// DeleteWhere finishes Core enumeration before any deletion. Each native delete
// constrains both _id and the complete observed metadata_json string with
// expression equality and binary collation. Changed metadata is retained. It
// provides no shared read snapshot or revision CAS; concurrent updates that
// preserve the same metadata value may still be deleted, and newly matching
// records may remain. Hosts requiring a snapshot must coordinate writers.
// DeleteIDs validates and deduplicates the whole input before explicit identity
// deletion. Native mutation failures and invalid acknowledgments stay errors.
//
// Breaking replacement removes Dimensions, Similarity and its enum,
// InitializeSchema, configurable storage fields and MetadataFieldsToFilter.
// Rebuild old BSON metadata documents into the current metadata_json string and
// provision the native vector index through the host SDK. There are no numeric
// adapters, old-format reads, migrations or schema-creation branches.
//
// Default tests are offline. Integration tests require SCOPE_MONGODB_URI for a
// deployment with Vector Search and create isolated databases and search indexes,
// poll only fixture readiness and visibility, then drop databases and disconnect.
package mongodb
