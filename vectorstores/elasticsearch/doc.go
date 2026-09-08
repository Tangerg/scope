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
// See https://www.elastic.co/docs/reference for the full API.
package elasticsearch
