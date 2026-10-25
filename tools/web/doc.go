// Package web defines provider-neutral web search and page-fetching tools.
//
// [Searcher] and [Fetcher] are independent capabilities because not every
// provider supports both. A provider client may implement either or both;
// each provider owns one package and one transport client. [NewSearchTool]
// and [NewFetchTool] adapt those capabilities to the core tool contract.
// Requests and responses reject invalid UTF-8. Published times use Core's
// lossless timestamp admission before a search result can be returned by a tool.
// Provider clients validate their own result projections: malformed records and
// missing required result payloads fail, while an explicit empty search result
// remains valid. Requested fetched content must be present even when it is empty.
package web
