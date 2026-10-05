// Package qdrant exposes Qdrant through the Core vector-store capability interfaces. Documents
// are stored as points in a Qdrant collection (`{id, vector,
// payload}`); retrieval runs the collection's vector search.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Document IDs must be canonical decimal uint64 values or lowercase hyphenated
// UUIDs. Qdrant maps UUID aliases to one native point and renders a canonical
// UUID on read, so accepting aliases would overwrite distinct caller IDs and
// change their returned values. [Store.Index] and [Store.DeleteIDs] reject other
// spellings before embedding or mutation I/O; query results and scroll cursors
// follow the same ID rule. The native point ID is the sole stored identity.
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
// Metadata filtering scrolls the complete collection's payloads and applies
// filter.Match before any vector limit. Qdrant's native conditions merge scalar
// equality with array membership, and is_empty also includes empty arrays.
// Local evaluation preserves these distinctions and whole-string LIKE without
// changing the payload schema. The selected IDs constrain bounded vector
// queries, whose ranked results are merged. Filtered queries cost O(N) payload
// reads and O(N) ID bookkeeping. Returned IDs and payloads are revalidated;
// a hit outside the selected IDs or no longer satisfying the predicate fails
// the entire search rather than reducing the requested result set.
//
// Filtered deletion enumerates before deleting bounded ID batches. Qdrant does
// not provide a payload revision condition for this path: concurrent metadata
// changes between enumeration and deletion may be removed according to the
// value observed during enumeration. Applications requiring a snapshot or
// conditional deletion must coordinate writers. Search likewise observes
// enumeration and vector retrieval at separate times.
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
// See https://qdrant.tech/documentation/ for the full API surface.
// Metadata numbers use signed 64-bit integers where exact, otherwise doubles
// whose decimal JSON value round-trips without loss. Unrepresentable numbers
// are rejected at the payload boundary.
package qdrant
