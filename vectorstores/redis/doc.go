// Package redis implements the Core vector-store capabilities over native
// Redis search and HASH records. The host provisions the index and owns its
// native client, topology, credentials, retries, timeouts and lifetime.
// [NewStore] never creates, alters or recreates an index. RESP3 is required:
// RESP2 does not expose the warning channel needed to reject partial searches.
//
// The concrete index's FT.INFO definition owns its namespace, dimension,
// algorithm and distance metric. It must select one nonempty HASH prefix,
// without FILTER or aliases, and index embedding as FLOAT32 VECTOR with COSINE,
// L2 or IP distance. An optional content TEXT field is allowed; additional
// indexed fields are rejected with [ErrIncompatibleIndex]. Native index tuning
// belongs to the host. [StoreConfig] carries no competing schema settings.
//
// Each document is exactly one HASH at the native prefix plus its Core ID.
// It contains content, embedding (little-endian FLOAT32 bytes), and
// metadata_json (the Core metadata JSON string). There is no duplicate ID or
// expanded metadata representation. Metadata keys may have any Core-supported
// name, including the HASH field names. Null and empty metadata remain distinct,
// and JSON numbers retain their exact values. Media documents are rejected.
// Search rejects invalid or extra returned fields, malformed metadata,
// wrong vector dimensions, nonfinite FLOAT32 values and zero cosine vectors.
// Existing deployments must provision this current native schema and reindex
// their source documents; there is no legacy read path or schema conversion.
//
// Index prepares the complete request, all model batches and all FLOAT32 vectors
// before the first write. Each Lua write atomically replaces one whole HASH,
// removing superseded fields. A native I/O failure may leave earlier records
// published; the operation does not claim cross-record transactionality.
//
// Construction validates FT.INFO without reading records. Unfiltered Search
// issues native KNN directly. Filtered Search scans the complete namespace and
// Core filter.Match alone selects metadata membership. Bounded INKEYS groups
// restrict KNN; native distances are merged before TopK and the Core score
// threshold. No metadata keys or values enter query syntax. Only predicate scans
// cost O(N) record reads and key bookkeeping. Native ClusterClient enumeration
// visits every master; Ring cannot guarantee complete scanning and is rejected.
// Enumeration and indexing visibility do not provide a database snapshot.
//
// Search rejects native warnings, malformed envelopes, incomplete hit counts,
// unreadable records, repeated keys and changed filtered membership. Invalid
// distances fail instead of becoming apparently valid scores. Every error
// returns no response, including an error after earlier hits were decoded.
//
// DeleteIDs is idempotent and sends single-key DEL commands, including on a
// cluster. DeleteWhere finishes complete enumeration before deleting, and
// atomically compares each record's observed metadata bytes with Lua. A concurrent
// metadata change retains that record and fails the operation; earlier deletions
// may already have completed. The adapter never retries a stale comparison.
//
// Default tests are offline. Tests selected with -tags=integration require
// SCOPE_REDIS_ADDR and optionally SCOPE_REDIS_USERNAME/SCOPE_REDIS_PASSWORD.
// They create unique native indexes and remove those indexes and their records.
// Use an isolated Redis instance with native search support.
//
// See https://redis.io/docs/latest/develop/ai/search-and-query/vectors/ and
// https://redis.io/docs/latest/commands/ft.search/ for native contracts.
package redis
