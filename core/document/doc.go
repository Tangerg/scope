// Package document defines the canonical serializable [Document] content
// value shared by extraction, retrieval, and model-facing components.
//
// NewDocument requires text, media, or both. Metadata is JSON-safe and belongs
// to the document itself; query-specific relevance belongs to the vector-store
// match value. Clone provides an independent snapshot when ownership crosses a
// component boundary.
//
// [Formatter] is the one contract for rendering a document as text, shared by
// etl pipelines and rag context assembly. [TextFormatter] renders the text
// alone and rejects media it cannot carry. Extraction, splitting, identifier
// assignment, batching, and loading policy live in the separate etl module.
package document
