// Package vespa implements Core indexing, semantic search, and predicate deletion
// through Vespa's Document and Search APIs. The caller deploys the application
// package and owns its HTTP client's authentication and transport lifetime.
//
// Metadata has one representation: scope_metadata is a string containing the
// complete Core metadata JSON, including null versus an empty object, arbitrary
// keys, and exact nested numbers. No business metadata attributes, presence
// lists, or independently writable namespace fields are used. Every operation
// visits the configured native namespace and document type with [document],
// follows opaque continuations to completion, and validates the current three
// document fields before filtering or writing. Core filter.Match alone selects
// metadata. Visits are not a cross-request snapshot.
//
// The deployed schema requires Vespa 8.741.10 or newer and this shape (adapt the
// native tensor width and configured field/profile names to the application):
//
//	schema scope {
//	  document scope {
//	    field content type string {
//	      indexing: summary
//	    }
//	    field scope_metadata type string {
//	      indexing: summary
//	    }
//	    field embedding type tensor<float>(x[2]) {
//	      indexing: attribute | summary
//	      attribute { distance-metric: euclidean }
//	    }
//	  }
//	  field scope_documentid type string {
//	    indexing: documentid | attribute
//	    match { exact cased }
//	    attribute: fast-search
//	  }
//	  rank-profile scope_rank {
//	    inputs { query(q) tensor<float>(x[2]) }
//	    first-phase { expression: closeness(field, embedding) }
//	  }
//	}
//
// scope_documentid is outside the document block: Vespa derives it from the
// native document ID and refuses direct document writes to it. Identity therefore
// has no second writer. The default search summary must return the native
// documentid, unmodified content, complete scope_metadata, and dense tensor.
// [StoreConfig.RankingProfile] must rank solely by closeness(field, EmbeddingField),
// with finite relevance in [0,1]. The application package owns the native metric,
// tensor width, rank profile, and query limits. NewStore performs no I/O because
// Vespa exposes no standard schema API on this endpoint; existing source rows
// establish the observed tensor width, and native errors remain errors.
//
// Search uses exact nearestNeighbor over groups of at most 128 selected native
// IDs, requests every selected neighbor, validates every hit before MinScore,
// and merges native relevance before applying TopK. Full coverage with no
// degraded cause or query error is required. A silently capped query or missing
// selected document is an error, rather than a shortened successful result.
// This full-source approach favors exact Core semantics over native metadata
// indexes. It performs a linear visit and does not implement hybrid search.
//
// DeleteWhere sends each selected native ID with a test-and-set condition
// comparing the entire observed scope_metadata string. Vespa owns the atomic
// comparison and removal; a failed condition preserves the changed document.
// The operation does not retry by ID or reselect replacements. This is metadata
// protection, not a revision CAS for text or vector-only changes. Native failures
// can leave an earlier deletion prefix applied.
//
// Index validates IDs, text, batching, complete Core metadata, and all float32
// vectors before its first document put. Existing IDs are replaced by native put
// semantics. Each response must acknowledge the requested native ID. A later
// transport or native failure can leave an earlier put prefix applied. HTTP
// responses are bounded by MaxResponseBytes. The host controls timeouts and
// retries; this adapter adds none.
//
// The former metadata attribute schema and MaxHits configuration are removed.
// Deploy the current schema and reindex from the authoritative documents; old
// rows are rejected without compatibility reads or migrations.
//
// Native guarantees: https://docs.vespa.ai/en/reference/api/document-v1.html and
// https://docs.vespa.ai/en/writing/indexing.html#documentid-example.
package vespa
