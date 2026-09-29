// Package perplexity wires Perplexity's Search API into [web.Searcher].
//
// Requests use POST /search with a bearer token.
//
// [web.SearchRequest] maps to the Perplexity request:
//   - Query          → query
//   - MaxResults     → max_results (omitted when unset)
//   - AllowedDomains → search_domain_filter
//   - BlockedDomains → search_domain_filter entries prefixed with "-"
//   - Recency        → search_recency_filter
//
// Results map title → Title, url → URL, snippet → Snippet, and date →
// PublishedTime when it is a [time.DateOnly] date. Perplexity does not echo the
// query, so [web.SearchResponse.Query] is the caller's query.
//
// Reference: https://docs.perplexity.ai/api-reference/search-post
package perplexity
