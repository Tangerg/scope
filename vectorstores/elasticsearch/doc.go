// Package elasticsearch exposes the official go-elasticsearch v8
// client through the Core vector-store capability interfaces. Documents are indexed JSON
// objects with a `dense_vector` field for the embedding and a
// nested `object` field for metadata.
//
// Requirements: Elasticsearch 8.0+ for dense_vector + `knn`
// top-level query. The store uses the `knn` query (not
// `script_score`) for retrieval — that's GA since 8.4.
//
// Similarity functions: [SimilarityCosine] / [SimilarityL2] /
// [SimilarityDotProduct]. The chosen value is recorded in the
// dense_vector mapping at index creation time and cannot be changed
// without rebuilding.
//
// Search shape:
//
//	POST <index>/_search
//	{
//	  "size": K,
//	  "knn": {
//	    "field": "embedding",
//	    "query_vector": [...],
//	    "k": K,
//	    "num_candidates": ceil(K * NumCandidatesMultiplier),
//	    "filter": {"query_string": {"query": "<lucene>"}}
//	  }
//	}
//
// Filter visitor produces Lucene query-string syntax — metadata
// fields are addressed under `metadata.<key>` paths;
// LIKE wildcards (% / _) map to Lucene wildcards (* / ?).
//
// Search rejects a result that lost a targeted shard or timed out, because
// Elasticsearch answers with 200 and the surviving hits and a caller cannot
// otherwise tell a partial index from a small result.
//
// Delete uses _delete_by_query with the same Lucene filter. Elasticsearch
// reports version conflicts, per-document failures, and query timeouts inside a
// successful response, so an incomplete deletion returns an error while the
// documents it already removed stay removed.
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
// See https://www.elastic.co/docs/reference for the full API.
package elasticsearch
