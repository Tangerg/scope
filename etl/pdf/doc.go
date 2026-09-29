// Package pdf reads PDF payloads with github.com/ledongthuc/pdf, a pure-Go
// parser forked from rsc/pdf.
//
// The reader emits one document with the text of every page, or with
// [ReaderConfig.PerPage] one document per page carrying the 1-based
// [MetadataPageIndex]. Extraction is text-only: tables, columns, and unusual
// font encodings may come out in imperfect order, and scanned PDFs need OCR
// upstream.
package pdf
