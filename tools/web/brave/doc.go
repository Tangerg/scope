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
// description → Snippet. PublishedTime remains unknown because page_age can be
// either a publication or modification date; the relative age cannot disambiguate it.
//
// Reference: https://api-dashboard.search.brave.com/api-reference/web/search/get
package brave
