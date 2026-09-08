// Package qdrant exposes Qdrant through the Core vector-store capability interfaces. Documents
// are stored as points in a Qdrant collection (`{id, vector,
// payload}`); retrieval runs the collection's vector search.
//
// Requirements: a reachable Qdrant server (self-hosted or Qdrant
// Cloud). The store uses the official qdrant-client-go gRPC client.
//
// Vector similarity functions: cosine / dot / euclid / manhattan.
// The chosen value is bound to the collection at creation time.
//
// Filter visitor produces Qdrant's structured filter syntax —
// `{"must": [{"key": "author", "match": {"value": "Alice"}}]}`,
// `{"should": [...]}`, `{"must_not": [...]}` for NOT,
// `{"range": {"gte": 100, "lt": 200}}` for numeric ranges. The
// result feeds the `Filter` field of the search request.
//
// Payload. Qdrant's `payload` is arbitrary JSON; the store maps the
// document's text + metadata into the payload verbatim. Indexed
// payload fields (for filter performance) live on the collection
// schema and are configured out of band — the store does not create
// or modify them.
//
// Write visibility. Qdrant acknowledges an update as soon as it reaches the
// write-ahead log unless the request asks to wait. Every store write — index
// and both delete paths — waits for the change to be applied, so a Search
// issued after a write observes it.
//
// See https://qdrant.tech/documentation/ for the full API surface.
package qdrant
