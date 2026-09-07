// Package serper wires Serper's Google Search API into
// [web.Searcher].
//
// # Endpoint
//
// POST https://google.serper.dev/search
//
// Authentication uses the X-API-KEY header (not bearer).
//
// # Parameter mapping
//
// [web.SearchRequest] → Serper request:
//   - Query          → q (after Google site:/-site: rewriting via
//     [web.SearchRequest.QueryWithSiteOperators])
//   - MaxResults     → num (forwarded as-is)
//   - AllowedDomains → inlined into q as `(site:foo.com OR site:bar.com)`
//   - BlockedDomains → inlined into q as `-site:foo.com -site:bar.com`
//   - Recency        → tbs=qdr:h|d|w|m|y
//
// Serper has no native include/exclude domain fields, so the query
// is rewritten with Google's site:/-site: operators. The trade-off is
// that the operator count counts against Google's per-query token
// budget.
//
// Hardcoded: autocorrect=true.
//
// # Response mapping
//
// Serper organic[] → []*[web.SearchResult]:
//   - title   → Title
//   - link    → URL
//   - snippet → Snippet
//   - date    → PublishedTime (best-effort parse; relative strings
//     like "2 days ago" become zero time)
//
// Knowledge graph, answer box, people-also-ask, and related searches
// are ignored — only organic results are surfaced.
//
// Serper's transport DTOs stay private; only organic results cross the
// provider boundary.
//
// # Reference
//
// https://serper.dev/playground
package serper
