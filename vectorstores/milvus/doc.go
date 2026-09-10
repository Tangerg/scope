// Package milvus exposes Milvus / Zilliz Cloud
// through the Core vector-store capability interfaces. Documents are stored as rows in a Milvus
// collection (`{id, content, embedding, <metadata columns>}`);
// retrieval runs Milvus's ANN search.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Requirements: a reachable Milvus 2.x server (self-hosted, Docker,
// or Zilliz Cloud managed service). The store uses the official
// milvus-sdk-go/v2 gRPC client.
//
// Vector similarity functions: cosine / L2 / IP. The chosen value
// is bound to the collection's index at creation time; switching
// requires rebuilding the index.
//
// Schema. Milvus is strongly typed — every metadata field that
// participates in filters must be declared as a typed column at
// schema-creation time. [StoreConfig.MetadataFields] enumerates the
// columns; anything outside that set goes into a flexible JSON
// field that can still be filtered but at a higher cost.
//
// Filter visitor produces Milvus's expression language —
// `author == "Alice" and (year > 2020 or tag in ["a","b"])`. The
// result feeds the `expr` parameter of the search call.
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
// Null tests are refused. Milvus' expression syntax documents no IS NULL and
// no way to test whether a JSON key is present — its JSON operators are
// JSON_CONTAINS and its variants — so an IS NULL filter fails rather than
// being approximated by a value comparison that would answer differently.
//
// See https://milvus.io/docs for the full API surface.
package milvus
