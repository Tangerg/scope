// Package elasticsearch exposes the official go-elasticsearch v8
// client through the Core vector-store capability interfaces. Documents are indexed JSON
// objects with a `dense_vector` field for the embedding and a
// nested `object` field for metadata.
//
// Requirements: Elasticsearch 8.0+ for dense_vector + `knn`
// top-level query. The store uses the `knn` query (not
// `script_score`) for retrieval — that's GA since 8.4.
//
// The v8 client is deliberate, not a stale pin. Elastic's clients are forward
// compatible only — they "support communicating with greater or equal minor
// versions of Elasticsearch" — and every 8.x Go client enables REST API
// compatibility by default, so v8 reaches a 9.x server while v9 sends
// compatible-with=9 and cannot serve an 8.x one. Moving to v9 would narrow
// which servers this store works against and gain nothing.
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
// Filterable keys. A metadata key is written into the Lucene query as text,
// and query_string cannot quote a field name, so a filter can only name a
// key that is a plain identifier. An indexed key is a string literal in the
// filter DSL, so without that limit metadata['a:1 OR b'] compiled to
// metadata.a:1 OR b and the caller's key became a term boundary and a
// boolean operator. A document whose metadata key is anything at all still
// stores and reads back fine; this is only about which keys a filter can
// name.
//
// See https://www.elastic.co/docs/reference for the full API.
package elasticsearch
