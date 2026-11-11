// Package elasticsearch implements the Core vector-store capabilities using
// a borrowed native Elasticsearch SDK client. The host provisions the concrete
// index and owns the client, credentials, topology, retries, timeouts and lifetime.
// [NewStore] admits the native mapping and settings without reading documents
// and returns the shared [elastic.Store]; it never creates or modifies an index. The native index owns dimensions,
// similarity, vector index tuning and result-window policy. Native KNN also owns
// its candidate defaults; Scope does not reproduce a candidate multiplier.
//
// The index must declare exactly content (text), embedding (indexed FLOAT32
// dense_vector with explicit dimensions), and metadata_json (keyword with
// index:false and doc_values:false), under dynamic:strict. Complete stored
// _source is required. Reads explicitly include vectors, including when
// Elasticsearch 9.2+ excludes them from the default source projection.
// Aliases, required or per-document custom routing, runtime
// fields, dynamic templates and ingest pipelines are incompatible. The host must
// preserve these namespace and storage guarantees while Store is in use.
//
// Native cosine, l2_norm, dot_product and max_inner_product similarities are
// supported. The adapter validates the complete FLOAT32 vectors, including native
// finite-magnitude and unit-vector constraints, before publication. Native scores
// are projected through Core; group merging uses native scores before Core
// normalization so saturated scores do not change ranking.
//
// [elastic.Store] owns the record layout, namespace binding, bulk writes,
// scans and ranking described in its package; this adapter supplies the
// Elasticsearch engine: dense_vector admission, similarity constraints, score
// projection and query DSL. The Host owns the native transport. Offline tests
// exercise HTTP protocol fixtures; tagged tests require a separately
// provisioned isolated service.
package elasticsearch
