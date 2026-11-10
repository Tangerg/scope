# PDF parser provenance

This package is derived from `github.com/ledongthuc/pdf` at commit
`5e46805f718e` (2026-10-20), itself derived from `rsc/pdf`.
The upstream BSD license is preserved in `LICENSE`. `glyph_generate.go` generates
`glyph_unicode.go` from the bundled Adobe Glyph List at
`../../testdata/adobe-glyphlist.txt`; the generated header records its SHA-256.

Scope uses this single internal parser because source byte and page limits
cannot bound cross-reference traversal, decompression, object allocation, or
font-map expansion. The fork adds one per-read resource grant shared by the
lexer, cross-reference loader, stream filters and text interpreter. Resource
exhaustion and cancellation survive parser recovery and discard the entire
extraction. Cross-reference and recursive object cycles are rejected directly.
No timer or abandoned parsing goroutine is used.

Only the value decoding and plain text extraction surface is retained. Layout,
outline and styled-text APIs are omitted. Scope also validates stream errors,
AES padding and PDF text operand syntax instead of accepting partial decoding.
When updating the parser, retain all resource checks and run the parser-budget and malformed-input tests
alongside extraction and password regressions. `ParseBudget` in the public PDF
package owns default policy; this internal package receives resolved limits.
