// Package exa integrates Exa's Search and Contents APIs with the
// provider-neutral web contracts.
//
// A [Client] implements both web.Searcher and web.Fetcher. Search requests use
// POST /search; page fetching uses POST /contents. Exa transport DTOs, search
// tuning, and response normalization remain private to this package.
// Fetch supports Markdown and HTML. Plain text is rejected because Exa's
// non-HTML extraction returns Markdown. Fetch requests fresh content so the
// requested rendering options take effect.
package exa
