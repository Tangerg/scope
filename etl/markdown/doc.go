// Package markdown extracts and transforms Markdown documents.
//
// Reader can emit one whole document or heading-scoped documents with source
// metadata. TextSplitter is the etl.TextSplitter for structure-aware,
// token-bounded chunking: headings
// are repeated as retrieval context, tables split only between rows, lists only
// between items, and fenced code blocks only between lines while retaining
// their fences.
package markdown
