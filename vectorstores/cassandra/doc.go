// Package cassandra implements Core vector-store capabilities with Apache
// Cassandra 5.0+ and the official Apache GoCQL v2 driver. The host owns the
// session and its consistency, authentication and native retry policies.
//
// A Store owns an exclusive table with exactly four columns: document ID as
// the sole text primary key, text content, the complete metadata.Map as JSON
// text, and a vector<float,N>. Construction checks actual native column types
// and primary-key shape. CreateDimensions seeds schema creation; operations
// obtain their width from native protocol metadata. No SAI index is required.
//
// Core's codec preserves nil versus {}, explicit null, nested JSON and exact
// numeric values. Core filter.Match alone decides membership and type errors.
// Search and DeleteWhere validate the entire observed table before query
// embedding, relevance thresholds, result caps or deletion. Native vector
// codecs bind values rather than injecting vector literals into CQL. Native
// similarity functions own vector validity and relevance ordering.
//
// Search scores captured vectors with similarity_cosine, similarity_euclidean
// or similarity_dot_product, applies MinScore, then TopK. Cosine and Euclidean
// native scores use their validated [0,1] range. Native dot scores equal
// (1+inner_product)/2; Core's inner-product conversion projects that value into
// [0,1]. Raw native similarity retains ordering when the projected score
// saturates. Equal native similarities use document ID byte order. Reassess
// existing dot-product MinScore values after upgrading.
//
// These exact operations scan the full table and retain matching records in
// memory. Ranking uses captured content, metadata and vectors consistently;
// Cassandra does not provide a collection snapshot. DeleteWhere compares all
// captured non-key columns in a native conditional DELETE and reports a
// concurrent replacement instead of deleting it. Predicate or scan failure
// causes zero deletions. Individual successful writes and deletions can precede
// a later transport failure, cancellation or CAS conflict; neither operation
// provides a transaction across partitions. Media is rejected before I/O.
//
// Breaking replacement: Session now accepts Apache GoCQL v2 queries, Dimensions
// becomes CreateDimensions, and MetadataColumn declarations/MetadataColumns
// and their SAI compiler are removed. Old tables with typed metadata columns
// fail the current schema contract; rebuild a four-column table from source
// documents and remove obsolete indexes under the host's migration policy.
// There is no legacy schema reader, column arbitration or compatibility shim.
package cassandra
