// Package couchbase implements Core vector-store capabilities through Couchbase
// Server 8.0+ KV, Query and Index services. The host supplies and owns a connected
// gocb cluster, an existing bucket/scope/collection, an embedding model and a
// document batcher. InitializeSchema creates the default primary query index;
// construction verifies the native vector-distance function and query access.
// Search Service and a vector index are not required.
//
// Identity belongs to the KV key. Document bodies contain exactly content,
// metadata and embedding. Metadata is a string containing the complete Core
// metadata JSON encoding, preserving null versus {}, exact numbers and nested
// values. The collection must contain only this current record format.
// Media documents are rejected before model or storage I/O.
//
// Search reads every current record with request_plus consistency and replica
// reads disabled. Core's filter evaluator determines membership and failures
// before query embedding, independently of TopK and MinScore. Each selected
// record's content, metadata and vector are captured together; subsequent writes
// cannot replace the record that was evaluated. This is a read projection, not
// a collection-wide transactional snapshot. Matching records are held in memory.
// The Query Service computes exact VECTOR_DISTANCE over their vector projections
// in batches of 512. Full scans trade indexed approximate-search throughput for
// complete Core selection and failure semantics.
//
// Scores use Core's documented cosine-distance, Euclidean-distance and negative
// inner-product-distance conversions. Native distances own ranking even when
// score conversion rounds different distances to the same score. Equal native
// distances are ordered by exact KV key bytes.
// Invalid or incomplete native results fail instead of becoming successful hits.
// See https://docs.couchbase.com/server/current/n1ql/n1ql-language-reference/vectorfun.html.
//
// Index upserts one KV record at a time after encoding its complete batch.
// DeleteWhere validates the entire collection before any mutation, then removes
// the evaluated CAS versions. A concurrent replacement causes a CAS error and
// remains stored. Unknown IDs are ignored by DeleteIDs. Mutations are individual
// KV operations: transport, cancellation or CAS errors may leave earlier writes
// or deletions applied. They do not provide a multi-document transaction. Native
// key limits are enforced before mutation; IDs are not truncated or normalized.
// Named collections allow 246-byte keys; the default scope's default collection
// allows 250-byte keys. KV mutations use the SDK's default durability.
//
// Breaking migration: rebuild the collection and reindex source documents.
// The former body ID, metadata object and FTS record/index format are rejected;
// no dual read or conversion path is provided. Remove the old FTS index and its
// VectorIndexName, Dimensions and IndexOptimization configuration. Similarity
// values are the native COSINE, L2 and DOT metrics; MinScore thresholds must be
// reassessed against the Core distance conversions.
package couchbase
