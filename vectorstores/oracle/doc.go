// Package oracle indexes precomputed vectors in a Host-provisioned SQL table.
// Upsert and ranked queries recheck the durable space marker and native fixed
// vector dimension. Deletion uses the table selected by the Host directly.
// A positive CreateDimensions provisions a missing table, and an existing
// table must already declare that width; no model, tokenization or embedding call occurs inside the adapter.
// Configured schema, table and column names use Oracle's uppercase identity;
// SQL quotes that canonical name so reserved words remain valid identifiers.
// Content requires a non-null CLOB in an AL32UTF8 database. Construction checks
// the character set before optional schema creation, and native bindings recheck
// it before admission and writes because driver conversion can silently replace
// unsupported Unicode characters. Metadata retains exact encoded bytes in BLOB.
//
// Query performs native nearest-neighbor ranking without enumerating metadata.
// The stored metadata representation cannot implement exact Core predicates
// natively, so a filtered Query fails with vectorstore.ErrUnsupportedFilter.
// A Host authorized for the whole table can explicitly QueryByScan or DeleteMatching
// using a finite vectorstore.ScanBudget. In the scan transaction, the adapter
// bounds source records and the ID/metadata bytes of its two passes before
// reading their payload; native ranking, protocol overhead and server work are
// not included in the payload bound. On exhaustion there is no partial result.
//
// Upsert prepares every vector before issuing writes. One failed row reports
// prior native acknowledgments, its unknown status and subsequent unattempted
// rows. The Host owns the DB handle and any decision to reconcile or retry.
// Rebuild unmarked tables; do not label previously unverified coordinates.
package oracle
