// Package milvus exposes Milvus / Zilliz Cloud
// through the Core vector-store capability interfaces. Documents are stored as rows in a Milvus
// collection ({id, content, vector, metadata, metadata_filter});
// retrieval runs Milvus's ANN search.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Requirements: a reachable Milvus 2.x server (self-hosted, Docker,
// or Zilliz Cloud managed service). The store uses the official
// github.com/milvus-io/milvus/client/v2 gRPC client.
//
// Vector similarity functions: cosine / L2 / IP. The chosen value
// is bound to the collection's index at creation time; switching
// requires rebuilding the index.
//
// Schema. Construction always describes and checks the existing collection's
// required field types, primary key, string capacities, vector dimension, and
// vector index metric. Complete metadata occupies the non-nullable JSON field
// named metadata. metadata_filter is a required non-nullable JSON projection
// derived from that same metadata and written in the same Upsert. Construction
// rejects collections missing either field; it never adds an old-schema reader
// or rewrites existing data. Existing deployments must rebuild the collection
// and reindex their original documents.
// Dimensions may be zero when attaching to a collection; the actual dimension
// is then retained for validating subsequent embeddings. Creating a collection
// requires explicit dimensions and never calls the embedding model. With
// InitializeSchema disabled, construction performs only read operations and
// the caller is responsible for loading the collection before search.
//
// Filter selectors address document metadata. Keys named id, content, vector,
// metadata, or metadata_filter remain metadata keys; they never address physical
// columns. Search and DeleteWhere share one compiler, while DeleteIDs addresses
// the native id primary key. Selectors are encoded as JSON tuples, retaining the
// distinction between array indices and object keys such as 0 and "0".
//
// Filter ownership. Native JSON comparison can retain UNKNOWN for missing keys
// and coerce numbers through double. metadata_filter instead carries non-null
// paths, type paths, scalar values, and array members derived only from metadata.
// Every atomic value condition requires its corresponding type path before
// composition or negation. IS NULL negates non-null presence, including the
// presence of empty arrays and objects. Search reads only the complete metadata
// object, never reconstructing it from projections. Nil indexed metadata writes
// an empty object; null or malformed stored metadata is refused on read.
//
// Numeric scalar encodings retain exact decimal value and ordering using native
// string comparison. Integer and fractional IN members and HAS members reuse
// the same scalar encoding, without native numeric coercion. Numeric metadata
// must be representable by math/big.Rat, the decoder used by Core; unsupported
// values are refused before I/O.
// Numeric ordering requires numeric metadata and LIKE requires strings. Their
// compiled failure condition retains Core's left-to-right logical short circuit.
// Search and DeleteWhere query at most one failing row before embedding or
// deletion, then evaluate its complete metadata through Core to return the error.
// Final selection also excludes failing rows. HAS on a non-array returns false.
// ID literals are escaped before reaching the native deletion expression;
// quotes, backslashes, and query-looking text remain part of the ID.
// String projections encode each Unicode character as a fixed-width ASCII
// token. LIKE uses the same encoding for its literal parts: % spans tokens and
// _ spans one token, preserving characters, newlines, and literal backslashes
// without relying on native byte matching. Literal patterns use equality.
//
// Upsert acknowledgment. Milvus answers an upsert with the number of rows it
// accepted; Index requires that count to match what it sent rather than
// treating a short write as a complete one.
//
// Scoring. The three metrics report three different quantities, so each has its
// own mapping. COSINE is a similarity in [-1, 1]. L2 is the squared distance —
// Milvus stops before the square root — which still ranks correctly. IP is the
// raw inner product with no normalization, so it is unbounded unless the caller
// supplies unit vectors and cannot share the cosine mapping.
//
// See https://milvus.io/docs for the full API surface.
package milvus
