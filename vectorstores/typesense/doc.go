// Package typesense adapts a provisioned Typesense 30.2 collection to Core Indexer,
// Searcher and IDDeleter. The host owns collection provisioning, SDK client
// configuration, credentials, transport, retries and client lifetime. Store
// uses the official SDK's raw HTTP operations, owns every returned response
// body, and propagates native HTTP errors, read errors and close errors.
//
// NewStore reads the actual collection name, fields and vector dimension. The
// collection must store indexed content:string, metadata:string and indexed
// embedding:float[] with a positive num_dim and cosine distance. The native
// cosine default is accepted. An optional id:string declaration is allowed;
// other fields, native auto-embedding and incompatible schemas are rejected.
// Scope does not create collections or carry a competing dimension setting.
//
// Each record contains exactly id, content, metadata and embedding. The native
// id is the canonical unpadded URL-safe base64 encoding of the Core ID; there
// is no second ID payload. Metadata is the complete Core JSON value encoded
// as a string, preserving nil versus empty maps and exact nested numbers.
// Existing collections using raw IDs or object metadata must be replaced and
// reindexed from their authoritative documents. There is no legacy decoder.
//
// Construction and search validate a complete JSONL document export. Core
// filter.Match alone selects metadata membership; selected native keys restrict
// the same native search operation before TopK. Returned keys and metadata must
// still satisfy that membership. A malformed record, changed membership,
// repeated hit, cutoff or incomplete page fails the whole search. These reads
// are not an atomic snapshot; concurrent writers require host coordination.
// Export permission is required even for unfiltered search. Every response,
// including the complete export, must fit MaxResponseBytes, which defaults to
// DefaultMaxResponseBytes. Hosts must size this explicit budget for their data.
//
// Semantic search preserves native ANN ranking across pages of at most
// MaxResultsPerPage and projects cosine distance through the Core score
// contract. Approximate search can find fewer than TopK documents. Hybrid
// search submits both lexical and vector evidence, preserves native fusion
// order and projects the rank to 1/(rank+1). HybridAlpha optionally controls
// native vector weight. Hybrid TopK cannot exceed MaxResultsPerPage: native
// keyword candidate pools and fusion ranks change between pages. Larger hybrid
// requests return Core ErrInvalidOptions before I/O. Curation is disabled so
// it cannot independently insert ranked hits.
//
// Index validates all documents and metadata, embeds every batch and checks
// every float32 vector against the native schema before publishing the first
// import. Media documents and vectors that become nonfinite or all zero are
// rejected. Upsert replaces the complete record. Each import must acknowledge
// every input exactly once with success=true, even when HTTP status is 200.
// Native I/O or acknowledgment failure can leave earlier accepted writes;
// multi-batch indexing is not atomic.
//
// DeleteIDs deduplicates explicit IDs and deletes by their encoded native keys.
// Unknown IDs are idempotent. Store does not implement FilterDeleter because
// Typesense cannot condition deletion on the revision observed by Core's
// predicate. Hosts must choose explicit IDs under their own writer coordination
// when predicate-based deletion is required.
//
// See https://typesense.org/docs/30.2/api/vector-search.html and
// https://typesense.org/docs/30.2/api/documents.html. Native integration tests
// require SCOPE_TYPESENSE_ENDPOINT and SCOPE_TYPESENSE_API_KEY and create and
// remove only uniquely named test collections. Default tests are offline.
package typesense
