// Package neo4j implements Core vector-store capabilities with Neo4j 5.18+
// and the official Go driver. The host owns the driver and its connection pool.
// A Store owns its sessions and an exclusive document label. Construction
// verifies an actual uniqueness constraint on the document ID property and the
// selected native vector similarity function; InitializeSchema creates only
// that constraint. Existing vector indexes are neither consulted nor required.
//
// Each node contains exactly four configured properties: the original document
// ID, text, the entire metadata.Map encoded as a JSON string, and the embedding
// as a list of FLOAT values. The Core codec preserves nil versus {}, explicit
// null, nested values, numeric spellings and escaped strings. Media is rejected
// before model or database I/O. Graph relationships and other labels remain
// native graph facts; DETACH DELETE removes relationships of deleted nodes.
//
// Core filter.Match alone decides membership and failure. Search reads and
// validates every observed node before query embedding, minimum score or TopK.
// It then asks vector.similarity.cosine or vector.similarity.euclidean to score
// the captured vectors, applies MinScore, and returns at most TopK results.
// Equal native scores use document ID byte order. Native scores already lie in
// [0,1] and are validated without clamping; they are not portable thresholds.
// Exact search scans the full label and retains matching records in memory;
// it exchanges approximate index acceleration for the complete Core contract.
//
// Index embeds all batches before a single retryable native write transaction,
// so publication is atomic and SDK retries do not repeat model calls. DeleteWhere
// takes native node write locks before reading properties, validates the full
// observed collection, then deletes selected nodes in the same transaction.
// Predicate or stream failure rolls back all changes. Locked nodes cannot be
// replaced during deletion. Neo4j read-committed isolation does not promise a
// collection snapshot or include nodes created after the scan. Search uses
// captured properties and vectors consistently across model calls.
//
// Breaking replacement: MetadataPrefix, IndexName, Dimensions and their index
// creation configuration are removed. MetadataProperty names the single JSON
// property. Old flattened nodes fail strict validation; rebuild an exclusively
// owned label from source documents and remove obsolete vector indexes under
// the host's migration policy. There is no legacy reader or in-place converter.
package neo4j
