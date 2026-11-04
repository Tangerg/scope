// Package pdf extracts text from PDF payloads with a resource-bounded Go parser.
//
// The reader emits one document with the text of every page, or with
// [ReaderConfig.PerPage] one document per page carrying the 1-based
// Provenance.Page. Named inputs are parsed from the same bounded snapshot
// whose digest identifies their original revision. Extraction follows content
// stream order, including nested Form XObjects; table and column layout is not
// reconstructed, and scanned PDFs need OCR upstream.
//
// Font codes use ToUnicode when supplied. Otherwise StandardEncoding, WinAnsi,
// MacRoman and UniGB-UCS2-H are supported; unembedded standard Latin Type 1 fonts
// also have a known built-in StandardEncoding. Unrecognized encodings, embedded
// built-in encodings, unknown Differences glyphs and inherited ToUnicode CMaps
// cannot establish Unicode text with these decoders, so their pages are omitted
// with [ErrUnsupportedFontEncoding] wrapped in [ErrPartialRead]. Other readable
// pages remain available.
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
