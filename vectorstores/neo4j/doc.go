// Package neo4j stores precomputed document vectors on an exclusive Neo4j
// label. A named uniqueness constraint binds its database label, ID property,
// vector space and coordinate width. NewStore verifies this physical binding;
// it never relabels or silently adopts a foreign collection.
//
// Upsert validates all records and publishes them in one managed transaction.
// Retried transactions never call a model. An uncertain commit is reported as
// one uncertain group; a successful commit followed by session-close failure
// retains acknowledgments. The host owns the driver and its retry policy.
//
// The document schema does not imply a vector index. QueryByScan is the explicit,
// bounded exact search operation; it filters before ranking captured vectors
// with Neo4j's native scalar similarity. Chargeable source bytes are the
// element ID, property names, text/metadata strings, and float64 coordinates.
// DeleteMatching locks observed nodes, verifies the complete predicate and
// deletes within one transaction. A budget error rolls that operation back.
// DeleteIDs is a separate identity operation and never needs a scan.
package neo4j
