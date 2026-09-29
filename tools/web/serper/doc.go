// Package serper wires Serper's Google Search API into [web.Searcher].
//
// Requests use POST /search with the X-API-KEY header and always enable
// autocorrect.
//
// [web.SearchRequest] maps to the Serper request:
//   - Query      → q, with domain filters rewritten by
//     [web.SearchRequest.QueryWithSiteOperators]
//   - MaxResults → num (omitted when unset)
//   - Recency    → tbs=qdr:h|d|w|m|y
//
// Only organic[] results are surfaced: title → Title, link → URL,
// snippet → Snippet, and date → PublishedTime when it is an absolute date.
//
// Reference: https://serper.dev/playground
package serper
