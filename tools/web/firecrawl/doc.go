// Package firecrawl integrates Firecrawl's Search and Scrape APIs with the
// provider-neutral web contracts.
//
// A [Client] implements both web.Searcher and web.Fetcher. Search requests use
// POST /search with native includeDomains/excludeDomains filters, preserving
// the query text; page fetching uses POST /scrape and supports Markdown and HTML.
// The wire contract follows https://docs.firecrawl.dev/api-reference/endpoint/search.
package firecrawl
