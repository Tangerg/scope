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
// Existing collection. The collection is verified whenever it is found,
// whatever [StoreConfig.InitializeSchema] says, because that flag answers
// whether a missing collection may be created and not whether the one found is
// the right one — and the second question matters most for a collection
// provisioned out of band. A configured metric that disagrees with the
// collection's returns scores that are wrong rather than absent, so it fails
// construction with [ErrIncompatibleCollection], as does a collection that is
// neither found nor creatable. [StoreConfig.Dimensions] is compared only when
// declared, since it is required to create a collection and optional to attach
// to one.
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
// issued after a write observes it, and then reads the status of that wait.
// Only Completed means "update is applied and ready for search"; Acknowledged
// is "received, but not processed yet", WaitTimeout is a "timeout of awaited
// operations", and ClockRejected means the update was "rejected due to an
// outdated clock". The gRPC call succeeds under all four, so the status rather
// than the call is what establishes that the write happened.
//
// Null tests emit is_empty rather than is_null. Qdrant separates the two:
// is_null matches records where the field "exists and has NULL value", while
// is_empty matches records where it "either does not exist, or has null or []
// value". The filter AST treats an absent key and an explicit null alike, and
// an absent key is the ordinary case for metadata, so is_null would answer
// nothing for the documents an IS NULL test is usually asked about. is_empty
// is wider in one respect — it also matches a key holding an empty array,
// which the AST reports as non-null — and Qdrant offers no condition that
// separates that case.
//
// See https://qdrant.tech/documentation/ for the full API surface.
// Metadata numbers use signed 64-bit integers where exact, otherwise doubles
// whose decimal JSON value round-trips without loss. Unrepresentable numbers
// are rejected at the payload boundary.
package qdrant
