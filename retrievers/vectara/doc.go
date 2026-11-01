// Package vectara retrieves ranked passages from one Vectara corpus. Vectara
// owns chunking, embeddings and ranking. New checks the native corpus; Retrieve
// calls the single-corpus query endpoint once and never scans documents. Bind
// AllowedDocumentIDs to restrict both reads and explicit document writes.
// CreateDocument only creates; it returns ErrDocumentExists on a native conflict.
// DeleteDocument is a separate explicit operation. Query-relative scores and
// source/part metadata survive without lossy conversion.
package vectara
