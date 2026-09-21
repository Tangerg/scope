// Package brave wires Brave's Web Search API into [web.Searcher].
//
// # Endpoint
//
// GET https://api.search.brave.com/res/v1/web/search
//
// Authentication uses the X-Subscription-Token header (not bearer).
//
// # Parameter mapping
//
// [web.SearchRequest] → Brave query parameters:
//   - Query          → q (after Google-style site:/-site: rewriting)
//   - MaxResults     → count (default 10 when omitted)
//   - AllowedDomains → inlined as `site:foo.com` operators
//   - BlockedDomains → inlined as `-site:foo.com` operators
//   - Recency        → freshness=pd/pw/pm/py; hourly filtering is rejected
//
// Brave has no native allow/block-domain fields, so the query is
// rewritten with site:/-site: like the Serper provider does.
//
// # Response mapping
//
// Brave web.results[] → []*[web.SearchResult]:
//   - title       → Title
//   - url         → URL
//   - description → Snippet
//   - page_age    → PublishedTime (RFC3339; the human-readable
//     "age" field is dropped because relative strings like "2 hours
//     ago" don't parse)
//
// The provider deliberately owns and hides Brave's transport DTOs; only the
// normalized web result vertical crosses the package boundary.
//
// # Why Brave
//
// Brave runs its own independent index (not a Google/Bing reseller),
// so adding it gives the agent a result source that doesn't drift
// with the Google duopoly. Pricing is friendly: ~2000 queries/month
// free, sub-cent per query on paid tiers.
//
// # Reference
//
// https://api-dashboard.search.brave.com/app/documentation/web-search/get-started
package brave
