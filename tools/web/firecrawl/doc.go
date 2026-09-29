// Package firecrawl integrates Firecrawl's Search and Scrape APIs with the
// provider-neutral web contracts.
//
// A [Client] implements both web.Searcher and web.Fetcher. Search requests use
// POST /search; page fetching uses POST /scrape and supports Markdown and HTML.
package firecrawl
