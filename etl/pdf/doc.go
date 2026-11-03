// Package pdf extracts text from PDF payloads with a resource-bounded Go parser.
//
// The reader emits one document with the text of every page, or with
// [ReaderConfig.PerPage] one document per page carrying the 1-based
// Provenance.Page. Named inputs are parsed from the same bounded snapshot
// whose digest identifies their original revision. Extraction is text-only: tables, columns, and unusual
// font encodings may come out in imperfect order, and scanned PDFs need OCR
// upstream.
//
// UserPassword contains the producer-encoded user-password bytes for RC4 or
// AES-128 documents; it is copied at construction. Owner-password authentication
// and AES-256 are unsupported and are not approximated.
//
// SourceBudget limits encoded bytes, MaxPages limits declared pages, and
// ParseBudget limits lexer work, object allocation and expanded stream/text
// bytes across the complete read. Parsing runs on the caller's goroutine and
// observes cancellation during parsing and decoding. The borrowed ReaderAt
// must return from its own I/O calls; the parser cannot interrupt a blocked
// custom ReadAt implementation.
package pdf
