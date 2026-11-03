// Package etl provides explicit extract-transform-load building blocks for
// documents.
//
// Format readers extract source data into core document values. Formatters,
// splitting, identifier assignment, and batching transform those values for a
// downstream index. A Splitter applies one TextSplitter policy, such as
// SeparatorSplitter, TokenSplitter, or markdown.TextSplitter, to documents and
// owns chunk metadata and lineage for every policy. Named readers bind the source and revision to the original
// bytes in document.Provenance. Splitting appends a chunk to its parent's path,
// leaving business metadata untouched. Byte positions are present only where a
// reader can prove an exact half-open interval in the original input.
//
// [github.com/Tangerg/scope/etl/text.FileWriter] is a
// filesystem load target; semantic indexing composes ETL output with rag/index.Writer.
//
// The base module owns the root package plus the text, JSON, and Markdown
// packages. HTML and PDF are separate leaf modules because their parser
// dependencies are materially larger.
package etl
