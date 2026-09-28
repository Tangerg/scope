// Package opensearch exposes the official opensearch-go v4 client
// through the Core vector-store capability interfaces. Documents are indexed JSON objects with a
// `knn_vector` field for the embedding and a nested `object` field
// for metadata.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
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
// The returned mapping must explicitly identify that engine and agree with
// the configured engine. An omitted engine is not inferred from the current
// server default, which differs across OpenSearch versions.
//
// New and existing indices undergo the same mapping and source checks.
// Only concrete index names are supported. A name resolving to a different
// physical index is rejected because aliases can apply filtering and routing
// that must not be silently discarded.
//
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
//	    "filter": {"ids": {"values": ["<matched-id>"]}}
//	  }}}
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
// results by their original provider score, before Core score normalization
// can collapse distinct ranks. It reads all metadata before selecting TopK, costs O(N)
// metadata reads, and holds O(N) document IDs. Unfiltered Search remains a
// single native KNN request. A concurrent update between selection and KNN
// may change a document, so every returned ID must belong to its selected
// batch and its actual metadata must still satisfy the predicate. A mismatch
// or evaluation error fails the entire Search. No cross-request snapshot is
// promised, and updates that still satisfy the predicate can be returned.
// Filtered Search requires Lucene HNSW on OpenSearch 2.4+, Faiss HNSW on
// 2.9+, or Faiss IVF on 2.10+. NMSLib filtered Search returns
// errors.ErrUnsupported before embedding or search I/O; its unfiltered
// Search and DeleteWhere remain available.
//
// DeleteWhere uses the same complete selection and sends conditional bulk
// deletes with each document's sequence number, primary term, and custom
// routing. A document
// changed since selection causes a conflict instead of deleting its newer
// contents. Partial failures return errors; earlier deletions remain applied.
// Missing pages, missing concurrency tokens, repeated documents, shard
// failures, and incomplete acknowledgments never become successful results.
// Scroll cleanup uses a bounded context even when the caller cancels.
//
// See https://docs.opensearch.org/latest/search-plugins/knn/ for the
// k-NN plugin reference.
package opensearch
