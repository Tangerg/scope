// Package milvus exposes the official Milvus / Zilliz Cloud gRPC client through
// Core's Indexer, Searcher, FilterDeleter and IDDeleter capabilities. The host
// owns the client, authentication, retries, transport and lifetime. Scope never
// creates, loads, changes or closes native resources.
//
// Native policy. Provision and load a concrete collection with exactly four
// non-nullable fields: id VARCHAR as a caller-supplied primary key, content
// VARCHAR, metadata VARCHAR, and vector FLOAT_VECTOR. Disable automatic IDs,
// dynamic fields, default values, partition keys and embedding functions. The
// vector field requires exactly one finished COSINE, IP or L2 index. Every
// operation reads the collection's native capacities, dimension and index
// metric; no Scope configuration overrides them. Construction also reads and
// validates all current rows. It performs no embedding call.
//
// Current records. metadata contains one complete Core JSON value encoded as a
// string, retaining nil separately from an empty object, exact numbers, nested
// values and arbitrary metadata keys. Rows from the old JSON metadata and
// metadata_filter schema are rejected. Existing deployments must recreate the
// collection and reindex their original documents; there is no migration reader
// or compatibility adapter. InitializeSchema, Dimensions and MetricType have
// been removed from StoreConfig. Use the official SDK to provision resources.
//
// Filtering. Core's predicate evaluator owns metadata selection. Strong native
// source queries enumerate all current rows in primary-key order using typed
// cursor parameters. Even short pages are followed until an empty page. Source
// errors, repeated identities and malformed rows fail before selection can hide
// them. This full scan requires work proportional to the collection size and is
// not a transaction-wide snapshot under concurrent writes.
//
// Search. Only semantic mode is supported. Core selects identities before
// embedding; bounded groups are sent to native ANN searches with strong
// consistency and the current native metric. Native raw scores determine global
// TopK before conversion to Core's score scale. COSINE is similarity in [-1, 1],
// IP is an unbounded inner product, and L2 is squared distance. Every hit is
// validated before MinScore, including its complete metadata, identity, text
// and float32 vector. A selected identity whose current metadata no longer
// matches the predicate fails the search instead of returning a stale match.
// Native topK is limited to 16,384; approximate indexes retain native recall.
//
// Indexing. Media is unsupported. The complete request, metadata capacities,
// batches and all embeddings are prepared before the first upsert. Vectors
// must fit the native width, remain finite in float32 and be nonzero for COSINE.
// A native upsert must acknowledge the exact count and identities sent. A later
// native failure can leave an earlier batch stored; no rollback or retry hides
// this outcome.
//
// Deletion. DeleteWhere selects through Core and submits native conditions on
// both ID and the complete observed metadata string. The native strong delete
// owns the condition and mutation; changed metadata retains the document. This
// does not promise a cross-request snapshot or compare text/vector revisions.
// DeleteIDs expresses literal ID intent, validates the whole input, deduplicates
// it and ignores unknown identities. No deletion falls back to unguarded IDs.
//
// See https://milvus.io/docs/v2.6.x/get-and-scalar-query.md and
// https://milvus.io/docs/v2.6.x/delete-entities.md for native operation contracts.
package milvus
