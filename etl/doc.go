// Package etl provides explicit extract-transform-load building blocks for
// documents.
//
// Format readers extract source data into core document values. Formatters,
// splitters, identifier assignment, and batching transform those values for a
// downstream index. [github.com/Tangerg/scope/etl/text.FileWriter] is a
// filesystem load target; vector-store loading belongs to core/vectorstore.
//
// The base module owns the root package plus the text, JSON, and Markdown
// packages. HTML and PDF are separate leaf modules because their parser
// dependencies are materially larger.
package etl
