// Package vectara implements Core indexing, semantic search, and ID deletion
// through Vectara's current v2 HTTP API. The host owns corpus provisioning,
// credentials, transport, and HTTP client lifetime. Vectara owns embedding and
// native ranking; no Scope model, distance setting, OAuth refresh, or retry layer
// competes with that policy.
// The native corpus must be enabled, store documents, and have no custom score
// dimensions. This policy is reread before every complete source operation.
//
// The current source has one native document ID, one core document part containing
// the complete text, and exactly one document metadata field, metadata_json. Its
// value is the complete Core JSON string. Numbers, arbitrary keys, nested values,
// null metadata, and empty objects survive without conversion to native scalar
// filter types. Media, multiple parts, separate context, tables, and images are
// unsupported and rejected. Native IDs remain the sole stored identity.
//
// NewStore, Index, and Search enumerate the complete native corpus, using opaque
// page keys until no key remains, and retrieve every full document by ID. Repeated
// cursors or IDs and malformed records fail, including documents that a predicate
// or MinScore would exclude. Construction requires document read permissions and
// reports an inaccessible or invalid corpus immediately. The full scan costs
// one policy request, one listing request per page, and one retrieval request per
// document; retained data is proportional to the selected source documents.
//
// # Search
//
// Core filter.Match is the sole metadata predicate evaluator. Filtered searches
// restrict native ranking to selected doc.id values in bounded expressions using
// Vectara's built-in ID filter, requiring no custom filter attributes. All identity
// expressions are prepared before querying and must fit the native 8,000-character
// limit. Native raw relevance determines global TopK before MinScore; every hit
// must have a current text result shape, unique identity, complete metadata, and
// unchanged Core membership. Any error returns no partial response.
//
// Requests disable lexical interpolation, reranking, query rewriting, generation,
// streaming, and saved history. Vectara's bounded semantic relevance [-1,1] maps
// linearly to Core [0,1]; non-finite or out-of-range values are errors. Native text
// parts are explicitly supplied by Index, each mapping directly to a search result;
// the adapter does not rely on server-side chunking or return snippets as documents.
//
// # Writes and deletion
//
// Index validates and prepares the complete request and batcher output before
// mutation. Vectara's document creation endpoint rejects existing IDs, so each
// replacement uses native ID deletion followed by creation with wait_for=searchable.
// This is not atomic: readers can observe the gap, and a failed creation can leave
// the previous document deleted. A confirmed creation must return HTTP 201 and the
// requested native ID. Failed native operations are returned without retries.
// DeleteIDs validates all IDs first, deduplicates them, and ignores only native
// not-found; every other status remains a failure. Earlier IDs can be removed if
// a later deletion fails. The native service owns its document-change conflicts.
//
// # Breaking contract
//
// DeleteWhere and the FilterDeleter capability, MetadataPrefix, DefaultAPIVersion,
// and the metadata DSL compiler have been removed. Native filter deletion is best
// effort and can miss matching documents through indexing lag; selecting IDs and
// deleting them unconditionally also loses the original metadata condition. These
// paths cannot implement Core's complete predicate deletion contract. Explicit
// ID deletion is a separate caller intent, not a predicate deletion substitute.
//
// Rebuild or reindex the corpus through the host before construction when its
// records do not have the current complete JSON and single-part shape. No legacy
// reads, metadata projections, default-prefix aliases, or schema migrations remain.
//
// Default tests exercise the current HTTP protocol offline. Integration tests
// require SCOPE_VECTARA_URL and SCOPE_VECTARA_API_KEY with corpus creation/deletion
// permissions and create disposable corpora. Managed-service checks require those
// credentials; offline protocol tests do not demonstrate native cloud execution.
// Current protocol references:
// https://docs.vectara.com/docs/rest-api/list-corpus-documents
// https://docs.vectara.com/docs/rest-api/get-corpus-document
// https://docs.vectara.com/docs/rest-api/bulk-delete-corpus-documents
// https://docs.vectara.com/docs/learn/metadata-search-filtering/ootb-metadata-filters
package vectara
