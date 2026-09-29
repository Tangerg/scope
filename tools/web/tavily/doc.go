// Package tavily integrates Tavily Search and Extract with the
// provider-neutral web contracts.
//
// A [Client] implements both web.Searcher and web.Fetcher. Search requests use
// POST /search; page fetching uses POST /extract and supports Markdown and plain
// text.
package tavily
