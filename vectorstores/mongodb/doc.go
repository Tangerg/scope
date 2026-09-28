// Package mongodb exposes MongoDB Atlas Vector Search
// through the Core vector-store capability interfaces. Documents are stored as ordinary BSON
// documents (`{_id, content, metadata, embedding}`); retrieval runs
// the `$vectorSearch` aggregation stage.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Metadata numbers use BSON integers when integral and representable as int64,
// otherwise doubles only when their decimal value survives JSON round-tripping.
// Unrepresentable values are rejected before writing rather than rounded.
//
// Requirements: a MongoDB deployment that provides $vectorSearch and the
// Search Indexes API. The store uses the official v2 Go driver.
//
// Vector similarity functions: [SimilarityCosine] /
// [SimilarityEuclidean] / [SimilarityDotProduct]. The chosen value
// is recorded in the Atlas Vector Search index definition.
//
// Indexes. Atlas Vector Search indexes are NOT regular MongoDB
// indexes; they're managed via the Search Indexes API and live on
// dedicated Atlas search nodes. The store creates one automatically
// under [StoreConfig.InitializeSchema] = true, including any
// metadata fields enumerated in
// [StoreConfig.MetadataFieldsToFilter] as typed `filter` paths.
//
// Metadata filtering reads the complete collection's metadata with an
// aggregation cursor and applies filter.Match before any vector limit. Native
// equality and IN can match array elements, while Atlas's vector prefilter
// supports a smaller operator set than ordinary MongoDB queries. The store
// therefore passes bounded lists of selected IDs to $vectorSearch and merges
// their ranked results. Filtered queries cost O(N) metadata reads and memory
// for matches. The vector index must include _id as a filter path; new indexes
// include it, and existing indexes must be updated before filtered searches.
// MetadataFieldsToFilter still controls additional native filter index paths.
//
// Enumeration completes before filtered deletion. Each deletion is conditional
// on the observed BSON metadata value (or its absence), using expression
// equality and binary collation so an array or case variant cannot match the
// observed document. A changed metadata value is retained. Enumeration and the later
// vector query are separate observations, not a transactional snapshot.
// Returned IDs and metadata are revalidated; a hit outside the selected IDs or
// no longer satisfying the predicate fails the entire search.
//
// Search pipeline:
//
//	{$vectorSearch: {...}}, {$addFields: {score: {$meta: "vectorSearchScore"}}},
//	{$match: {score: {$gte: minScore}}}
//
// Candidate pool. numCandidates sizes the priority queue the search fills, so
// Atlas requires it to be at least the requested result count and at most
// [MaxNumCandidates]. [StoreConfig.NumCandidates] is a recall floor the store
// raises to cover TopK; a TopK above the ceiling is refused rather than sent.
//
// Upsert acknowledgment. One bulk write reports MatchedCount for the
// replacements that found an existing document and UpsertedCount for those that
// inserted one; Index requires their sum to cover the batch. An unacknowledged
// write concern (w: 0) is rejected rather than reported as success, because
// MongoDB then answers with no reply at all and the driver's counts carry no
// information.
//
// See https://www.mongodb.com/docs/atlas/atlas-vector-search/.
package mongodb
