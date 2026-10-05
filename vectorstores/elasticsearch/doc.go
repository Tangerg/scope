// Package elasticsearch implements the Core vector-store capabilities using
// a borrowed native Elasticsearch SDK client. The host provisions the concrete
// index and owns the client, credentials, topology, retries, timeouts and lifetime.
// [NewStore] reads the native mapping and settings and validates current records;
// it never creates or modifies an index. The native index owns dimensions,
// similarity, vector index tuning and result-window policy. Native KNN also owns
// its candidate defaults; Scope does not reproduce a candidate multiplier.
//
// The index must declare exactly content (text), embedding (indexed FLOAT32
// dense_vector with explicit dimensions), and metadata_json (keyword with
// index:false and doc_values:false), under dynamic:strict. Complete stored
// _source is required. Aliases, required or per-document custom routing, runtime
// fields, dynamic templates and ingest pipelines are incompatible. The host must
// preserve these namespace and storage guarantees while Store is in use.
//
// Native cosine, l2_norm, dot_product and max_inner_product similarities are
// supported. The adapter validates the complete FLOAT32 vectors, including native
// finite-magnitude and unit-vector constraints, before publication. Native scores
// are projected through Core; group merging uses native scores before Core
// normalization so saturated scores do not change ranking.
//
// Each source record contains exactly three fields and uses the native _id as
// its sole identity. Metadata is one Core JSON string rather than native mapped
// terms. Nil and empty metadata remain distinct; arbitrary Core-supported keys,
// nested values, large integers and decimal values preserve their exact meaning.
// Media is unsupported. Native IDs are limited to 512 UTF-8 bytes. Existing
// deployments must provision the current schema and reindex their source
// documents; obsolete fields, configuration and record formats are not accepted.
//
// Index prepares all records, embedding batches, native vectors and NDJSON before
// its first bulk publication. A validation or model failure publishes nothing.
// Native I/O failures may leave earlier bulk records published; no cross-record
// transaction is claimed. Every bulk acknowledgment must identify the requested
// operation, index and document, with a valid status and no hidden failure.
//
// Search reads a complete native scroll snapshot before KNN retrieval. Every
// record is validated, and Core filter.Match alone decides metadata membership.
// Filtered KNN queries use bounded native ID selections, then merge native ranks
// before TopK and the Core score threshold. This costs O(N) source reads and
// document-ID bookkeeping. Enumeration and later KNN retrieval are separate
// requests; returned membership is revalidated and no cross-request snapshot is
// promised. Native TopK limits are checked before I/O.
//
// Timeout, failed or missing shard acknowledgments, malformed records, repeated
// identities, truncated pages, missing concurrency tokens and unreadable hits
// fail the operation. Search returns no response on any error. Scroll cleanup
// uses a bounded context even after caller cancellation; cleanup failures remain
// visible. [StoreConfig.MaxResponseBytes] bounds every response and defaults to
// [DefaultMaxResponseBytes]; response bodies are always closed.
//
// DeleteIDs deduplicates IDs and treats unknown IDs as successful no-ops.
// DeleteWhere enumerates the complete metadata snapshot before writing, then
// uses native sequence-number and primary-term conditional deletes. A changed
// or vanished snapshot member fails rather than deleting a newer version or
// silently succeeding. Earlier deletions may remain applied after a failure.
//
// Default tests are offline. Tests selected with -tags=integration require
// SCOPE_ELASTICSEARCH_ENDPOINT and optionally SCOPE_ELASTICSEARCH_USERNAME and
// SCOPE_ELASTICSEARCH_PASSWORD. They create and delete unique native indexes;
// run them only against an isolated instance. Native verification exercises
// Elasticsearch 8.19.7 and its current KNN and stored-source contracts.
//
// See https://www.elastic.co/guide/en/elasticsearch/reference/8.19/dense-vector.html
// and https://www.elastic.co/guide/en/elasticsearch/reference/8.19/search-search.html.
package elasticsearch
