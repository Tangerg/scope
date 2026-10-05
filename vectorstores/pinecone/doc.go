// Package pinecone adapts ready dense serverless Pinecone vector indexes to the
// Core Indexer, Searcher, IDDeleter, FilterDeleter and Closer capabilities.
//
// NewStore requires an existing index named by StoreConfig.IndexName and reads
// its host, dimensionality and metric through the native SDK DescribeIndex.
// Missing policy, unsupported schemas and control-plane authorization failures
// are errors. The host provisions the index and owns API credentials, native
// transport configuration and SDK client lifetime. Store opens one native index
// connection and Close releases that connection. Native Namespace() owns the
// effective namespace, including the SDK's default namespace normalization.
//
// The current native record has exactly two metadata string fields: content
// carries document text and metadata_json carries metadata.Map.MarshalJSON's
// complete value. The native vector ID alone owns document identity. No metadata
// keys are expanded into native fields. Core numbers, nested values, arrays,
// arbitrary keys, null and nil versus empty metadata survive without a second
// codec. Documents containing media are rejected before embedding or upsert.
// Native record IDs and namespaces must be ASCII without NUL, at most 512
// characters; IDs must be nonempty. Identity is preserved without escaping.
//
// Construction and every search read the complete namespace through native list
// and fetch. Every source record must satisfy the current schema, including
// records outside a predicate or relevance threshold. Core filter.Match alone
// decides predicate membership. Filtered searches query native metadata_json
// string membership in bounded groups; both filtered and unfiltered searches use
// native ranking. Groups merge by raw native scores before TopK and Core score
// normalization. Cosine uses Core cosine normalization, dotproduct uses Core
// inner-product normalization, and Euclidean uses Core distance normalization.
// Scores, dimensions and complete native records are validated before returning
// any results. The adapter supports semantic search only.
//
// DeleteWhere first completes Core selection, then issues native conditional
// deletions by the selected complete metadata_json strings. The native deletion
// excludes indexed metadata values outside that selection. These
// operations have no cross-request snapshot or revision compare-and-swap: newly
// matching values not selected by the scan may remain, and records with the same
// selected metadata value may be deleted regardless of text or vector changes.
// Native delete acknowledgments contain no per-record counts. Hosts needing
// stronger snapshot guarantees must coordinate writers. DeleteIDs is the
// explicit idempotent identity deletion capability; it validates all IDs and
// deduplicates them before publishing batches of at most 1,000 IDs.
//
// Index validates and encodes all records, completes every model batch and
// validates every dense FLOAT32 vector before its first upsert. Native upserts
// contain at most MaxVectorsPerUpsert records and must acknowledge every vector.
// Native I/O failures may leave earlier batches published; the adapter does not
// retry, roll back, or claim transactional indexing. Pinecone's metadata size,
// request size and query result size limits remain native errors. Full namespace
// scans require list/fetch permissions, O(N) record reads and O(N) identity and
// metadata tracking. Filtered query merging stores up to O(groups * TopK) hits.
// List, fetch and query are eventually consistent and provide no shared snapshot.
//
// Breaking replacement: remove IndexHost, DistanceMetric and its public metric
// enum. Supply Client and IndexName, and provision native policy through the host.
// Legacy expanded metadata records are rejected; rebuild the namespace with the
// two current string fields. There are no old-format reads, authorization
// fallbacks, metadata migrations or local vector-distance algorithms.
//
// Default tests are offline. Integration tests require Pinecone Local plus
// SCOPE_PINECONE_LOCAL_ENDPOINT and SCOPE_PINECONE_LOCAL_INDEX_COSINE,
// SCOPE_PINECONE_LOCAL_INDEX_DOTPRODUCT and SCOPE_PINECONE_LOCAL_INDEX_EUCLIDEAN,
// naming independently provisioned ready 2-dimensional indexes. They use isolated
// namespaces and clean them up. Pinecone Local's API 2025-01 control response
// omits vector_type, so these tests exercise native data-plane behavior directly;
// current API 2025-04 SDK construction is verified by offline HTTP/gRPC fixtures.
// Local also rejects serverless metadata-filter deletion; its test verifies
// explicit failure and retained records, without an ID-deletion fallback. The
// separate cloud deletion test requires SCOPE_PINECONE_API_KEY and
// SCOPE_PINECONE_INDEX_NAME for a ready 2-dimensional serverless index and uses
// an isolated namespace. Offline tests verify the conditional SDK wire request
// and selection/update ordering; Local does not establish cloud deletion parity.
package pinecone
