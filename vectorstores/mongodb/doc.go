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
// Requirements: MongoDB Atlas (vector search isn't available on
// self-hosted Community / Enterprise — it's an Atlas-only feature).
// The store uses the v2 official driver
// (go.mongodb.org/mongo-driver/v2).
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
// Filter visitor produces MongoDB query-document syntax —
// `{"metadata.author": {"$eq": "Alice"}}`, `{"$and": [...]}`,
// `{"$nor": [...]}` for NOT, and an anchored `{"$regex": "^...$"}` for
// LIKE — anchored because LIKE matches the whole value, and without the "i"
// option because it is case-sensitive. The result feeds the `filter` field of
// `$vectorSearch`.
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
