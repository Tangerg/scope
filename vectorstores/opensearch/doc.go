// Package opensearch exposes the official opensearch-go v4 client
// through the Core vector-store capability interfaces. Documents are indexed JSON objects with a
// `knn_vector` field for the embedding and a nested `object` field
// for metadata.
//
// Requirements: OpenSearch 2.x+ with the k-NN plugin (built-in on
// every recent release).
//
// Space types — five distance variants are recognized; coverage
// depends on the engine:
//
//   - [SpaceTypeCosine] / [SpaceTypeL2] / [SpaceTypeIP] — supported
//     by all three engines (Lucene / NMSLib / FAISS);
//   - [SpaceTypeL1] / [SpaceTypeLInf] — NMSLib and FAISS only.
//
// Engines: [EngineLucene] (default, ships with core), [EngineNMSLib],
// [EngineFaiss]. The chosen value is baked into the index mapping
// at creation time and cannot be changed without rebuilding.
//
// Search uses approximate k-NN:
//
//	POST <index>/_search
//	{
//	  "size": K,
//	  "query": {"knn": {"embedding": {
//	    "vector": [...], "k": K,
//	    "filter": {"query_string": {"query": "<lucene>"}}
//	  }}}
//	}
//
// Filter visitor produces Lucene query-string syntax under the
// configured metadata prefix — same dialect as the Elasticsearch
// store, intentionally so callers can swap between the two.
//
// Result completeness. OpenSearch reports lost shards, query timeouts, version
// conflicts, and per-document failures inside a successful response. Search
// rejects a result missing any targeted shard, and filtered deletion rejects an
// incomplete deletion while the documents it already removed stay removed.
//
// Metadata mapping. Metadata keys are unknown when the index is created, so
// their fields map dynamically. The default for a JSON string is "text with a
// .keyword sub-field" and the text field is analyzed, which would make
// `metadata.author:"Alice"` a tokenized, case-insensitive match — it would
// match an author of "Alice Smith" or of "alice". A dynamic template maps
// strings under the metadata path straight to keyword instead, so the field
// the filter compiler queries is the whole-value, case-sensitive one, and the
// sub-field's ignore_above cutoff never applies. An index created before this
// mapping needs a reindex for filters to compare exactly.
//
// See https://docs.opensearch.org/latest/search-plugins/knn/ for the
// k-NN plugin reference.
package opensearch
