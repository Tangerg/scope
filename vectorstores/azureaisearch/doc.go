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
// support nested-property paths in $filter). LIKE maps to
// `search.ismatch('pattern', 'field')`; IN maps to
// `search.in(field, 'v1,v2,...', ',')`.
//
// Index and DeleteWhere share the document action endpoint and its
// 1000-action request limit. Each document's response must acknowledge its
// action; HTTP success alone does not establish that every action succeeded.
// Partial failures return an error while successful actions remain applied.
// Metadata cannot use the configured ID, content, or embedding fields, or
// protocol annotation names beginning with @. The entire Index request is
// checked for these conflicts before embedding or sending any batch.
//
// Vector profiles belong to the pre-provisioned index's vector field. Queries
// select that field and use its profile without a second store-level setting.
//
// See https://learn.microsoft.com/azure/search/vector-search-overview.
package azureaisearch
