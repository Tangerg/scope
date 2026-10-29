// Package pdf reads PDF payloads with github.com/ledongthuc/pdf, a pure-Go
// parser forked from rsc/pdf.
//
// The reader emits one document with the text of every page, or with
// [ReaderConfig.PerPage] one document per page carrying the 1-based
// [MetadataPageIndex]. Extraction is text-only: tables, columns, and unusual
// font encodings may come out in imperfect order, and scanned PDFs need OCR
// upstream.
//
// SourceBudget and MaxPages limit encoded bytes and declared pages. They do not
// bound expanded parser memory or interrupt an individual page decode. Hosts
// accepting untrusted PDFs need an execution boundary that enforces memory and
// time limits; the reader observes cancellation between parser calls.
package pdf
