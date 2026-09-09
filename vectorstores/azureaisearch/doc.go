// Package azureaisearch exposes Azure AI Search's vector capabilities
// through the Core vector-store capability interfaces over the REST API (Azure doesn't ship a
// typed Go SDK for the Search service yet).
//
// Requirements: an Azure AI Search service (Basic tier or higher),
// with an index pre-provisioned through ARM / Terraform / Portal /
// REST. The store does NOT create indexes — Azure AI Search index
// schemas are typed and declared at creation; scope assumes the
// configured ID / content / vector / metadata fields exist.
//
// Authentication: API key via the `api-key` header. For Managed
// Identity / OAuth, inject a bearer token through a custom
// [http.Client].
//
// Semantic search supplies one vector query. Hybrid search supplies the same
// vector together with `search` and restricts lexical evidence to the
// configured content field, leaving fusion to Azure AI Search.
// Search consumes server-provided continuation parameters before treating a
// query as complete. Returned metadata retains its JSON representation,
// including integers outside the exact float64 range.
//
// Vector request shape:
//
//	POST /indexes/<index>/docs/search?api-version=2024-07-01
//	{
//	  "top": K,
//	  "vectorQueries": [{"kind": "vector", "vector": [...],
//	                     "k": K, "fields": "contentVector"}],
//	  "filter": "<odata>"
//	}
//
// Filter visitor produces OData `$filter` syntax — metadata fields
// must exist as TOP-LEVEL index fields (Azure AI Search doesn't
// support nested-property paths in $filter). IN maps to
// `search.in(field, 'v1,v2,...', ',')`.
//
// LIKE is refused. Azure's $filter offers no string function to build a
// pattern match on — its only Boolean functions are geo.intersects, search.in,
// search.ismatch, and search.ismatchscoring — and the last two run an analyzed
// full-text query rather than matching a whole value. search.ismatch('Alice')
// matches an author of "Alice Smith" or "alice", and Azure's own example notes
// that searching "waterfront" also matches "water" and "front". Refusing keeps
// one filter from meaning tokenized, case-insensitive, substring matching here
// and whole-value, case-sensitive matching on every other store.
//
// Index and DeleteWhere share the document action endpoint and its
// 1000-action request limit. Each document's response must acknowledge its
// action; HTTP success alone does not establish that every action succeeded.
// Partial failures return an error while successful actions remain applied.
// Metadata cannot use the configured ID, content, or embedding fields, or
// protocol annotation names beginning with @. The entire Index request is
// checked for these conflicts before embedding or sending any batch.
//
// Filtered deletion. Azure identifies a document to delete by its key and
// offers no delete-by-filter, so DeleteWhere collects keys first. It cannot
// collect them with skip: Azure's own continuation is the request back with a
// skip added, a filter-only query scores every match 1.00 in what Azure calls
// "an arbitrary order", and paged results over a changing index are documented
// as unstable, with the example returning one document twice — the same event
// as another being returned never. A key never enumerated is a document never
// deleted under a call that reported success. The walk instead follows Azure's
// documented "sort order and range filter as a workaround for skip", ordering
// by the key and carrying `<key> gt <last>` into each following page, and ends
// only on an empty page because a short page is not evidence of the last one.
// That is why [NewStore] also requires the configured ID field to be the
// index's key with `filterable` and `sortable` set: the walk cannot run
// without them, and Azure states they "can only be enabled when a field is
// first added to an index".
//
// Vector profiles belong to the pre-provisioned index's vector field. Queries
// select that field and use its profile without a second store-level setting.
// Because the transformation below is metric-specific, [NewStore] reads the
// index definition and follows the vector field to its profile, the profile to
// its algorithm, and the algorithm to its metric, refusing a configured value
// that disagrees with [ErrIncompatibleIndex]. Azure treats an index definition
// as an object rather than content, so that read needs the admin key
// [StoreConfig.APIKey] already documents — a query key is scoped to
// /indexes/{name}/docs and answers 403 here, as does an Entra role without
// Microsoft.Search/searchServices/indexes/read. The same read catches a vector
// field that is absent or names no profile, which would answer every query
// with nothing.
//
// Scoring. @search.score is never the raw metric value; Azure transforms it so
// it falls monotonically as the match worsens. The cosine transformation and
// its 0.333 to 1.00 range are documented, so the store inverts them to recover
// the cosine. Azure publishes neither for dotProduct or euclidean, so those
// scores pass through clamped rather than through a formula the store guessed.
//
// Null tests emit `<field> eq null`, which OData documents as matching a field
// that "will be null if it was never set, or if it was explicitly set to null"
// — the same two states the filter AST reads as nil.
//
// Filterable keys. A metadata key is written into the query language as
// text, and that language cannot quote a field name, so a filter can only
// name a key that is a plain identifier. An indexed key is a string literal
// in the filter DSL, so without that limit a caller's key was read as
// syntax. A document whose metadata key is anything at all still stores and
// reads back fine; this is only about which keys a filter can name.
//
// See https://learn.microsoft.com/azure/search/vector-search-overview.
package azureaisearch
