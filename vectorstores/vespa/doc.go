// Package vespa stores precomputed vectors and searches an explicitly bounded
// source through the Vespa Document and Search APIs. The actual native
// document namespace is derived from the host's logical namespace, SpaceID
// and coordinate width. Upsert validates, narrows and encodes the complete
// batch before any write. It does not enumerate the existing corpus. Each
// confirmed response must name the original native ID; WriteError identifies
// acknowledged, uncertain and unattempted records.
//
// Metadata has one representation: scope_metadata stores the complete Core
// metadata JSON including null versus an empty object and exact numeric values.
// Inner JSON escapes preserve code points that Vespa forbids in native string
// fields. Unicode escape spelling can change; decoded metadata keys and values
// retain their original Unicode semantics.
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
// Vespa exposes no standard schema API on this endpoint. QueryByScan verifies every
// visited native tensor's width. A document endpoint and rank-profile name
// alone do not prove an ANN index, so this store does not implement Querier.
//
// QueryByScan follows every opaque visit continuation and validates the complete
// observed source before applying the predicate. It uses exact nearestNeighbor
// over groups of at most 128 selected native IDs, requests every selected neighbor, validates every hit before MinScore,
// and merges native relevance before applying TopK. Full coverage with no
// degraded cause or query error is required. A silently capped query or missing
// selected document is an error, rather than a shortened successful result.
// This full-source approach favors exact Core semantics over native metadata
// indexes. It performs a linear visit and does not implement hybrid search.
//
// DeleteMatching sends each selected native ID with a test-and-set condition
// comparing the entire observed scope_metadata string. Vespa owns the atomic
// comparison and removal; a failed condition preserves the changed document.
// The operation does not retry by ID or reselect replacements. This is metadata
// protection, not a revision CAS for text or vector-only changes. Native failures
// can leave an earlier deletion prefix applied.
//
// Upsert validates IDs, text, batching, complete Core metadata, and all float32
// vectors before its first document put. Existing IDs are replaced by native put
// semantics. Each response must acknowledge the requested native ID. A later
// transport or native failure can leave an earlier put prefix applied. HTTP
// responses are bounded by MaxResponseBytes. The host controls timeouts and
// retries; this adapter adds none.
//
// The scan grant covers every visited record and cumulative native JSON bytes,
// including empty continuation pages and ranking responses. Each HTTP read is
// capped by the smaller of the per-response limit and remaining scan bytes.
// The deadline covers the whole operation. Incomplete visits are never ranked.
//
// Native guarantees: https://docs.vespa.ai/en/reference/api/document-v1.html and
// https://docs.vespa.ai/en/writing/indexing.html#documentid-example.
package vespa
