// Package weaviate exposes Weaviate through the Core vector-store capabilities.
// It uses weaviate-go-client/v5 with a Weaviate server supporting GraphQL cursor
// listing and the configured retrieval mode. Documents store their text, vector,
// and complete metadata JSON. Media documents are rejected before indexing I/O.
//
// Semantic retrieval uses nearVector. Hybrid retrieval combines the supplied
// vector with lexical evidence from content using relative-score fusion;
// [StoreConfig.HybridAlpha] controls vector weight. The configured distance
// metric must agree with the class's vector index: cosine, dot, l2-squared,
// hamming, or manhattan.
//
// # Metadata filtering
//
// A filtered Search enumerates the class's original metadata JSON through
// GraphQL after cursors and evaluates the Core predicate for every object. It
// then restricts the native semantic or hybrid ranking query to all matching
// UUIDs before applying TopK. Scalar and array values, missing and null values,
// whitespace, nested paths, and JSON numbers therefore retain Core semantics.
// Actual ranking results must belong to the selected UUIDs and still satisfy
// the predicate; a mismatch fails the complete search rather than returning
// a shortened result. Unfiltered Search issues the native ranking query directly.
//
// Filtering costs a full metadata scan per call and memory proportional to the
// matching UUIDs. The final ranking request carries every matching UUID, so
// large selections remain subject to the server's request limits. The scan
// follows short pages until an empty page and rejects an invalid response or a
// cursor that does not advance. Missing, malformed, or non-object metadata JSON
// is an error; JSON null represents a document without metadata.
//
// DeleteWhere uses the same complete selection and then deletes UUIDs. No
// deletion starts until every page and predicate evaluation has succeeded.
// Scan, ranking, and deletion are separate requests without snapshot or revision
// preconditions. Hosts must coordinate concurrent writes when they require a
// stable selection. A deletion error can leave earlier UUIDs already deleted;
// missing UUIDs are ignored so an operation can be retried.
//
// # Class construction and migration
//
// New and existing classes must have content and metadata text properties and
// the configured distance metric. Content requires searchable word tokenization
// for hybrid retrieval. NewStore validates the actual schema after creation as
// well as when reusing a class; mismatches return [ErrIncompatibleClass].
// InitializeSchema controls whether a missing class may be created.
//
// MetadataProperties and MetadataProperty declarations have been removed. Remove
// them from StoreConfig: metadata JSON is now the only filtering representation.
// Existing complete metadata JSON remains usable, and old projected properties
// are ignored. Reindex objects whose metadata JSON is absent or malformed.
// Metadata field tokenizers and null-state indexes are not required for Core
// filtering; only the original JSON is evaluated.
//
// Index requires one SUCCESS acknowledgment per submitted object, including
// batches whose HTTP request succeeded. A partial batch failure returns an
// error while accepted objects remain stored.
//
// Official protocol references:
// https://docs.weaviate.io/weaviate/manage-objects/read-all-objects
// https://docs.weaviate.io/weaviate/api/graphql/filters
// https://docs.weaviate.io/weaviate/config-refs/collections
package weaviate
