// Package opensearch implements the Core vector-store capabilities using a
// borrowed native OpenSearch SDK client. The host provisions the concrete index
// and owns authentication, topology, retries, timeouts and client lifetime.
// [NewStore] validates native mapping and settings without reading documents;
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
// The native mapping must bind _meta.scope_vector_space to the Host's immutable
// encoder space. Changing that declaration is a rebuild, not permission to reuse
// an old index. The Host must coordinate physical index replacement; HTTP
// configuration checks and data requests are distinct operations.
//
// Upsert prepares all records and checks their float32 representations before
// publishing NDJSON in independent MaxBulkBytes groups. A WriteError records
// verified item acknowledgments, explicit rejections, groups not submitted and
// uncertain IDs. Successful ingestion is not a guarantee of search visibility.
// Upsert, DeleteIDs and DeleteMatching share the encoded MaxBulkBytes limit,
// including action framing and conditional-delete tokens. Every action is
// admitted before the first mutation, so an oversized later action cannot
// partially publish an otherwise invalid batch.
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
package opensearch
