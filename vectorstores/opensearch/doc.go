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
// An index that already exists is checked rather than taken on trust.
// OpenSearch derives _score from the vector field's own space — "(2 - d) / 2"
// for cosinesimil is not "1 / (1 + d)" for l2 — and innerproduct is the only
// space whose score runs above 1. A store configured for one space against a
// field built for another therefore either applies the inner-product inverse
// to a number it does not describe, or clamps unbounded inner-product scores
// onto Core's ceiling so an exact match and a mediocre one become the same
// value. [NewStore] reads the mapping and refuses a disagreement with
// [ErrIncompatibleIndex], which also covers a field that is absent, is not a
// knn_vector, or holds a different width. The space type is read from the
// field, then from its method — "this value can also be specified within the
// method" — and otherwise resolved to its documented l2 default; a field
// trained from a model states none of them, and is reported rather than
// assumed.
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
// Filterable keys. A metadata key is written into the Lucene query as text,
// and query_string cannot quote a field name, so a filter can only name a
// key that is a plain identifier. An indexed key is a string literal in the
// filter DSL, so without that limit metadata['a:1 OR b'] compiled to
// metadata.a:1 OR b and the caller's key became a term boundary and a
// boolean operator. A document whose metadata key is anything at all still
// stores and reads back fine; this is only about which keys a filter can
// name.
//
// See https://docs.opensearch.org/latest/search-plugins/knn/ for the
// k-NN plugin reference.
package opensearch
