// Package weaviate implements Core vector-store capabilities using the native
// weaviate-go-client/v5 client. The host owns credentials, transport, client
// lifetime, and collection provisioning. NewStore binds an existing collection;
// Scope neither creates its schema nor configures a competing distance policy.
//
// The collection requires one unnamed dense vector, vectorizer none, no tenants,
// and exactly two text properties: content with searchable word tokenization,
// and metadata with filterable field tokenization. Its native index owns cosine,
// dot, l2-squared, hamming, or manhattan distance. These requirements are checked
// against the actual schema without enumerating stored objects.
//
// Native UUIDs are the sole document identities. Index and DeleteIDs require
// lowercase hyphenated UUIDs and reject aliases before embedding or mutation.
// Metadata is one complete Core JSON string, preserving exact numbers, arbitrary
// keys, and the distinction between null and an empty object. Extra properties,
// invalid metadata, missing vectors, and inconsistent vector widths fail reads.
// Media documents are rejected before indexing I/O.
//
// Weaviate does not expose a schema-level vector dimension. Index derives a
// read-only width from the complete current source and prepares every model batch
// and finite FLOAT32 vector before publishing one native batch. Unfiltered Search
// lets the native index validate query dimensions and checks returned vectors
// against the query width. An empty collection provides no stored width, including
// one whose previous objects were deleted; the native index may reject a prepared
// batch against its retained dimension. Native batch errors may leave accepted
// objects stored. Every submitted UUID must have exactly one SUCCESS
// acknowledgment; missing or partial acknowledgments fail.
//
// # Search and filtering
//
// Unfiltered Search performs native ranking without enumerating objects. Filtered
// Search enumerates the complete source with vectors through REST after cursors.
// Core filter.Match alone selects membership. Enumeration follows short pages
// until an empty page and validates the records needed for that predicate. Only
// filtered search incurs a full scan and memory proportional to selected UUIDs.
//
// Semantic search uses native nearVector. Selected UUIDs are queried in bounded
// groups; raw native distance determines global TopK, independently of Core scores.
// Hybrid search uses one native relative-score fusion over the entire selection;
// independently normalized candidate groups cannot be merged into that fusion.
// HybridAlpha selects the native query weight; nil preserves the server default.
// The selection remains subject to native request limits. Native hybrid scores
// must already be finite values in [0,1]. All returned records are validated
// before applying MinScore, and any invalid hit fails the whole response.
//
// # Deletion and concurrent writes
//
// DeleteIDs is an explicit identity deletion and ignores only native not-found.
// FilterDeleter is unsupported because native batch deletion does not atomically
// guard metadata at mutation time.
//
// # Breaking contract
//
// DistanceMetric, InitializeSchema, and schema creation have been removed. Hosts
// must provision the current collection before construction. Rebuild collections
// with projected metadata properties or incompatible tokenization, and reindex
// records that do not have the current complete source shape. No legacy schema or
// numeric hybrid-score fallback is retained.
//
// Native integration tests require SCOPE_WEAVIATE_URL and create disposable
// collections on that isolated server. Protocol references:
// https://docs.weaviate.io/weaviate/manage-objects/read-all-objects
// https://docs.weaviate.io/weaviate/manage-objects/delete
// https://docs.weaviate.io/weaviate/config-refs/collections
package weaviate
