// Package elasticsearch exposes the official go-elasticsearch v8
// client through the Core vector-store capability interfaces. Documents are indexed JSON
// objects with a `dense_vector` field for the embedding and a
// nested `object` field for metadata.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
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
// An index that already exists is checked rather than taken on trust.
// Elasticsearch derives _score from the vector field's own metric —
// "(1 + cosine(query, vector)) / 2" is not "1 / (1 + l2_norm(query,
// vector)^2)" — so a store configured for one metric against a field built for
// another returns plausible scores in the wrong scale, with MinScore filtering
// by a threshold that means something else. [NewStore] reads the mapping and
// refuses a disagreement with [ErrIncompatibleIndex], which also covers a
// field that is absent, is not a dense_vector, holds a different width, or is
// mapped index:false and so "can only use exact brute-force search" rather
// than the knn query every Search sends. Nothing here can be repaired in
// place: neither similarity nor dims can be changed after the field exists.
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
//	    "filter": {"ids": {"values": ["<matched-id>"]}}
//	  }
//	}
//
// Filtering reads every document's stored metadata through a scroll snapshot,
// then evaluates the predicate with Core filter.Match. Native term indexes
// erase scalar/array distinctions and treat empty arrays like missing fields;
// exact source evaluation also avoids analyzer, wildcard, and query-syntax
// changes to a caller's predicate. Existing text or keyword metadata mappings
// therefore do not change filter semantics. Complete, unmodified _source is
// required; pruned, disabled, or reconstructed source is incompatible.
//
// Filtered Search sends bounded ID selections to native KNN and merges their
// ranked results. It reads all metadata before selecting TopK, costs O(N)
// metadata reads, and holds O(N) document IDs. Unfiltered Search remains a
// single native KNN request. A concurrent update between selection and KNN
// may change a document; returned metadata must still satisfy the predicate
// or the whole query fails. No cross-request snapshot is promised.
//
// DeleteWhere uses the same complete selection and sends conditional bulk
// deletes with each document's routing, sequence number and primary term. A document
// changed since selection causes a conflict instead of deleting its newer
// contents. Partial failures return errors; earlier deletions remain applied.
// Missing pages, missing concurrency tokens, repeated documents, shard
// failures, and incomplete acknowledgments never become successful results.
// Scroll cleanup uses a bounded context even when the caller cancels.
//
// See https://www.elastic.co/docs/reference for the full API.
// NewStore requires a concrete index name; aliases are rejected because their
// filters and routing cannot be dropped during multi-request selection. Both
// newly created and existing indices must preserve stored source. Returned
// filtered-search documents are revalidated; a predicate change fails the query.
package elasticsearch
