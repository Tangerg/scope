// Package typesense exposes Typesense's semantic and hybrid search
// through the Core vector-store capability interfaces. Documents are regular Typesense documents in
// a collection with id / content / metadata (nested object) / embedding
// (float[]) fields, reached through the official typesense-go v3
// client.
//
// Requirements: Typesense 0.25+ (vector search GA) — the store uses
// nested-object metadata which needs `enable_nested_fields=true` on
// the collection.
//
// Distance metric: cosine only. Typesense's vector search always uses
// cosine distance — the result `vector_distance` is in [0, 2] and the
// store maps it onto a higher-is-better score in [0, 1].
// Hybrid search supplies lexical and vector evidence together. Typesense owns
// the fused ordering; [StoreConfig.HybridAlpha] optionally controls vector
// weight, and Scope maps result rank to query-relative relevance.
//
// Schema bootstrap. When [StoreConfig.InitializeSchema] is true the
// store probes for the collection and creates it with the right
// fields + dimensionality if missing. Existing collections are
// trusted as-is.
//
// Import acknowledgment. Typesense answers the document import endpoint with
// HTTP 200 even when individual documents were rejected, so the store requires
// one successful per-document result for every document it sent. A rejected
// document returns an error while accepted documents in the same batch remain
// stored.
//
// Filter visitor produces Typesense `filter_by` syntax — `metadata.k:=
// v`, `metadata.year:>= 2020`, `metadata.tag:= [a,b]` (IN form). The
// metadata field is a nested object so keys are addressed under the
// configured prefix.
//
// NOT caveat. Typesense `filter_by` has no top-level NOT operator —
// the visitor rewrites `NOT (x op y)` into the operator's inverse
// (e.g. `NOT (year >= 2020)` → `metadata.year:< 2020`). NOT wrapping
// anything other than a single binary comparison is rejected.
//
// Scoring depends on the vector field's vec_dist. Typesense reports
// vector_distance without units, and the store reads it as a cosine distance,
// so InitializeSchema states vec_dist explicitly instead of relying on the
// provider default and rejects an existing collection that uses "ip" — an
// inner-product distance read as a cosine one produces plausible scores in the
// right range that rank results wrongly, which no later call can detect.
//
// See https://typesense.org/docs/latest/api/vector-search.html.
package typesense
