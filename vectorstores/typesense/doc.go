// Package typesense exposes Typesense semantic and hybrid search through the
// Core vector-store capability interfaces. Documents use id, content, metadata
// and embedding fields, reached through the official typesense-go v3 client.
// Documents containing media are rejected before indexing I/O.
//
// The collection uses nested-object metadata with enable_nested_fields=true
// and a cosine vector field. InitializeSchema creates that schema when
// requested; existing collections are checked for compatible vector distance.
// Semantic scores project cosine distance into [0, 1]. Hybrid search sends
// lexical and vector evidence together, keeps the native fused order, and
// assigns query-relative scores from global result rank. HybridAlpha optionally
// controls the native vector weight.
//
// Filters are evaluated with Core filter.Match over a complete JSONL metadata
// export. The matched document IDs restrict native ranking before TopK. This
// preserves scalar-versus-array equality, exact numbers, missing and null
// values, nested keys and LIKE semantics that native metadata filters cannot
// express. Filtered search and deletion require document export permission and
// read the entire collection. Export and search metadata retain JSON numbers
// without the SDK map projection's float64 rounding.
//
// Search sends its vector and full ID set in a multi_search POST body. It reads
// pages of at most MaxResultsPerPage hits, preserves the native ranking across
// pages, and requires curated hits to obey the filter. Returned IDs must belong
// to the selected set, and returned metadata is checked against the predicate
// again. A changed value, unexpected ID, predicate error or incomplete response
// fails the entire search; it never silently removes candidates after TopK.
//
// Typesense's ID filter parser trims ASCII edge spaces, treats a sole * as a
// wildcard even when quoted, and cannot reliably preserve embedded backticks
// or a trailing backslash in a quoted value. A filtered operation whose matched
// set contains one of those IDs returns an error before searching or deleting.
// Other IDs, including Unicode, internal spaces and commas, use backtick
// literals. This restriction does not narrow Index or unfiltered Search.
//
// DeleteWhere finishes the complete export, predicate evaluation and ID
// validation before deleting by the selected IDs. These operations do not
// isolate concurrent writes. Returned-metadata checks can detect changed
// candidates, but cannot establish an atomic snapshot; callers needing one
// must coordinate writers externally.
//
// Import requires one successful acknowledgment per sent document because
// Typesense can return HTTP 200 with individual document failures. Accepted
// documents in a partially rejected batch remain stored.
//
// See https://typesense.org/docs/30.2/api/vector-search.html,
// https://typesense.org/docs/30.2/api/documents.html#export-documents and
// https://typesense.org/docs/guide/tips-for-filtering.html#escaping-special-characters.
package typesense
