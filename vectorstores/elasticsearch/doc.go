// Package elasticsearch implements the Core vector-store capabilities using
// a borrowed native Elasticsearch SDK client. The host provisions the concrete
// index and owns the client, credentials, topology, retries, timeouts and lifetime.
// [NewStore] validates the native mapping and settings without reading documents;
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
// The native mapping must bind _meta.scope_vector_space to the Host's immutable
// encoder space. Changing that declaration is a rebuild, not permission to reuse
// an old index. The Host must coordinate physical index replacement; HTTP
// configuration checks and data requests are distinct operations.
//
// Upsert prepares all records and checks their float32 representations before
// publishing NDJSON in independent MaxBulkBytes groups. A WriteError records
// verified item acknowledgments, explicit rejections, groups not submitted and
// uncertain IDs. Successful ingestion is not a guarantee of search visibility.
//
// Query only sends native KNN; it never enumerates source records. The native
// mapping deliberately preserves full metadata as opaque unindexed bytes, so
// a filtered Query returns vectorstore.ErrUnsupportedFilter before rank I/O.
// A Host with authority over the entire physical index can explicitly call
// QueryByScan for interpreted predicates. QueryByScan bounds source records, cumulative
// encoded scroll response bytes (excluding ranking and cleanup), and elapsed
// time. Exceeding a budget returns no partial ranking. The adapter checks the
// native result window, every returned record and every shard acknowledgment.
// Scroll selection and native ranking are separate reads, not a cross-RPC snapshot.
//
// DeleteMatching requires the same explicit scan budget, completes selection
// first, and uses each selected sequence number and primary term in its bulk
// conditional delete. A conflict is an error; no blind identity deletion or
// framework retry follows. DeleteIDs expresses unconditional identity intent.
// The Host owns the native transport. Offline tests exercise HTTP protocol
// fixtures; tagged tests require a separately provisioned isolated service.
package elasticsearch
