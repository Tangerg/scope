// Package mariadb indexes precomputed vectors in a Host-provisioned SQL table.
// Upsert and ranked queries recheck the durable space marker and native fixed
// vector dimension. Deletion uses the table selected by the Host directly.
// Dimensions in StoreConfig seeds only a newly created column;
// no model, tokenization or embedding call occurs inside the adapter.
// Content and metadata require utf8mb4 TEXT-family columns; MariaDB's JSON alias
// is LONGTEXT. CheckDocuments and Upsert read both native byte capacities and
// reject the complete batch before writes, including JSON encoding expansion
// and stored provenance. The default content TEXT permits 65,535 UTF-8 bytes.
// Write and query coordinates must have a finite squared norm accumulated in
// float32, as required by MariaDB's native VECTOR type. Cosine additionally
// requires a positive norm because zero and squared-norm underflow make its
// distance undefined. Euclidean vectors may have a zero norm.
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
package mariadb
