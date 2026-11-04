// Package tidb indexes precomputed vectors in a Host-provisioned SQL table.
// Upsert and ranked queries recheck the durable space marker and native fixed
// vector dimension. Deletion uses the table selected by the Host directly.
// Dimensions in StoreConfig seeds only a newly created column;
// no model, tokenization or embedding call occurs inside the adapter.
//
// Query performs native nearest-neighbor ranking without enumerating metadata.
// The stored metadata representation cannot implement exact Core predicates
// natively, so a filtered Query fails with vectorstore.ErrUnsupportedFilter.
// A Host authorized for the whole table can explicitly Scan or DeleteMatching
// using a finite vectorstore.ScanBudget. In the scan transaction, the adapter
// bounds source records and the ID/metadata bytes of its two passes before
// reading their payload; native ranking, protocol overhead and server work are
// not included in the payload bound. On exhaustion there is no partial result.
//
// Upsert prepares every vector before issuing writes. One failed row reports
// prior native acknowledgments, its unknown status and subsequent unattempted
// rows. The Host owns the DB handle and any decision to reconcile or retry.
// Rebuild unmarked tables; do not label previously unverified coordinates.
package tidb
