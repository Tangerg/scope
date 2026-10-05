// Package opensearch implements the Core vector-store capabilities using a
// borrowed native OpenSearch SDK client. The host provisions the concrete index
// and owns authentication, topology, retries, timeouts and client lifetime.
// [NewStore] reads native mapping and settings and validates current records;
// it never creates or changes an index. Native mapping owns vector dimensions,
// space, engine, method and encoding; native KNN owns candidate tuning.
//
// The index must declare exactly content (text), embedding (FLOAT32 knn_vector
// with explicit dimensions and method engine), and metadata_json (keyword with
// index:false and doc_values:false), under dynamic:strict. Native KNN must be
// enabled. Full stored _source is required; both index.derived_source.enabled
// and index.knn.derived_source.enabled must be false. Aliases, custom routing,
// dynamic templates, runtime or derived fields, and default ingest or search
// pipelines are incompatible. The host preserves these namespace and policy
// guarantees while Store is in use. Trained model_id fields are unsupported
// because their dimension and space are not declared by the mapping.
//
// Native Lucene and Faiss support l2, cosinesimil and innerproduct. Existing
// NMSLib mappings also support l1 and linf, but their filtered KNN returns
// errors.ErrUnsupported before I/O because native NMSLib cannot accept a KNN
// filter. HNSW and declared Faiss IVF methods are recognized; provision and
// training belong to the host. Omitted native space follows its documented l2
// default. Store never compares native policy against a Scope-configured metric.
//
// The adapter checks FLOAT32 coordinates, declared dimensions and the native
// cosine prohibition on zero vectors. Faiss SQ FP16 and automatic 2x encoding
// also require indexed coordinates in [-65504,65504] unless native clipping is
// enabled. Query vectors retain their native FLOAT32 contract; an index encoder
// does not impose its coordinate limit on queries. Scope never clips or silently
// repairs an embedding. Native scores are projected through Core; group merging
// uses native scores before Core normalization so saturated scores preserve rank.
//
// Every source has exactly three fields; native _id is its sole identity.
// Metadata is one Core JSON string rather than native mapped terms. Nil and
// empty metadata remain distinct. Arbitrary Core-supported keys, nested values,
// large integers and decimals preserve their meaning. Media is unsupported.
// Native IDs are limited to 512 UTF-8 bytes. Existing deployments must provision
// the current schema and reindex source documents; obsolete configuration and
// record formats are not accepted.
//
// Index validates all documents, embedding batches, native vector constraints
// and NDJSON before its first bulk publication. A preparation or model failure
// publishes nothing. Native I/O failures may leave earlier records published;
// no cross-record transaction is claimed. Every bulk acknowledgment must
// identify the requested operation, concrete index and document without failure.
//
// Search reads a complete scroll snapshot before native KNN. Every record is
// validated and Core filter.Match alone decides metadata membership. Filtered
// KNN uses bounded native ID selections and merges ranks before TopK and the
// Core score threshold. Enumeration costs O(N) source reads and ID bookkeeping.
// Enumeration and retrieval are separate requests; returned membership is
// revalidated and no cross-request snapshot is promised. Native TopK and result
// window limits are checked before I/O.
//
// Timeout, early termination, failed or missing shard acknowledgments,
// malformed records, repeated identities, incomplete pages, missing concurrency
// tokens and unreadable hits fail the operation. Search returns no response on
// error. Scroll cleanup uses a bounded context after cancellation; cleanup
// failures remain visible. [StoreConfig.MaxResponseBytes] bounds every response
// and defaults to [DefaultMaxResponseBytes]. Store closes every response body.
//
// DeleteIDs validates the whole input and deduplicates IDs; unknown IDs succeed
// as no-ops. DeleteWhere enumerates the complete metadata snapshot before
// writing, then uses native sequence-number and primary-term conditional
// deletes. Changed or vanished members fail instead of deleting a newer version
// or inventing success. Earlier deletions may remain applied after a failure.
//
// Default tests are offline. Tests selected with -tags=integration require
// SCOPE_OPENSEARCH_ENDPOINT and optionally SCOPE_OPENSEARCH_USERNAME and
// SCOPE_OPENSEARCH_PASSWORD. They create and delete unique native indexes;
// run them only against an isolated instance. Native verification exercises
// OpenSearch 3.3.2, Lucene and Faiss, exact metadata, filtered group ranking,
// native FP16 constraints and concurrent conditional deletion.
//
// See https://docs.opensearch.org/3.3/mappings/supported-field-types/knn-vector/
// and https://docs.opensearch.org/3.3/query-dsl/specialized/k-nn/index/.
package opensearch
