// Package opensearch implements the Core vector-store capabilities using a
// borrowed native OpenSearch SDK client. The host provisions the concrete index
// and owns authentication, topology, retries, timeouts and client lifetime.
// [NewStore] admits the native mapping and settings without reading documents
// and returns the shared [elastic.Store]; it never creates or changes an index. Native mapping owns vector dimensions,
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
// [elastic.Store] owns the record layout, namespace binding, bulk writes,
// scans and ranking described in its package; this adapter supplies the
// OpenSearch engine: knn_vector admission, space and encoding constraints,
// score projection and query DSL. The Host owns the native transport. Offline
// tests exercise HTTP protocol fixtures; tagged tests require a separately
// provisioned isolated service.
package opensearch
