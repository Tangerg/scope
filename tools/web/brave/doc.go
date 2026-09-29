// Package brave wires Brave's Web Search API into [web.Searcher].
//
// Requests use GET /web/search with the X-Subscription-Token header.
//
// [web.SearchRequest] maps to Brave query parameters:
//   - Query          → q, with domain filters rewritten as site:/-site: operators
//   - MaxResults     → count (10 when omitted)
//   - Recency        → freshness=pd/pw/pm/py; hourly filtering is rejected
//
// Brave web.results[] map to [web.SearchResult]: title → Title, url → URL,
// description → Snippet, and page_age → PublishedTime when it is RFC 3339.
// The relative "age" field is ignored.
//
// Reference: https://api-dashboard.search.brave.com/app/documentation/web-search/get-started
package brave
