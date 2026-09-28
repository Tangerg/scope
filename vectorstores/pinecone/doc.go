// Package pinecone exposes Pinecone through the Core vector-store capability interfaces.
// Documents are stored as vectors in a Pinecone index
// (`{id, values, metadata}`); retrieval runs the index's similarity
// query.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Metadata numbers use Pinecone's double representation only when their
// decimal value survives JSON round-tripping. Unrepresentable values are
// rejected before upsert rather than rounded or converted to strings.
//
// Requirements: a Pinecone account and an existing index (created
// via the Pinecone console or control-plane API — Pinecone does not
// allow lazy index creation from the data plane). The store uses
// the official pinecone-io/go-pinecone v4 client.
//
// Vector similarity. Pinecone configures cosine / dotproduct / euclidean at
// index-creation time; the store reads but does not override. Because the
// metric decides what a raw score means, [NewStore] reads the index's own
// metric from the control plane and refuses a configured value that disagrees
// with [ErrIncompatibleIndex] — a mismatch would otherwise return scores that
// are wrong rather than absent, with MinScore filtering by the wrong
// direction.
//
// A key with custom permissions may be denied the control plane, which Pinecone
// documents as the reason a caller "must target your index by host when
// performing data operations" — the shape [StoreConfig.IndexHost] already has.
// Construction does not demand that permission: an authorization denial leaves
// the configured metric unverified, while any other failure is reported.
// Dimensionality is never compared: this store declares none, and Pinecone
// rejects a wrong-width vector on the first request.
//
// Filtered operations list the complete namespace and fetch original metadata
// before applying Core filter.Match. This preserves scalar versus collection
// membership, exact string matching, and missing-field semantics. The List
// endpoint is available only for serverless vector indexes; failures remain
// explicit. Filtered Search computes exact scores from all matching float32
// vectors, using the index metric (euclidean means squared L2), then selects
// TopK. Unfiltered Search uses native approximate retrieval.
//
// Filtering therefore costs O(N) record reads, with O(N) identity tracking
// plus O(TopK) search results. It requires list/fetch permissions. These APIs
// are eventually consistent and do not promise a multi-request snapshot.
//
// Document text. Pinecone itself stores only id + vector + flat
// metadata — there is no first-class text body. The store always stashes
// the original document text under a reserved metadata key; retrieval
// reverses the mapping back into [document.Document.Text].
//
// Upsert acknowledgment. Pinecone answers an upsert with the number of vectors
// it accepted; Index requires that count to match what it sent rather than
// treating a short write as a complete one.
//
// DeleteWhere finishes selection before sending ID deletions in batches of
// 1,000. Pinecone has no conditional revision delete; hosts must coordinate
// concurrent writers when selection must stay true until deletion. A failed
// operation reports an error and earlier completed batches remain deleted.
//
// Metadata must follow Pinecone's flat format: strings, exactly representable
// numbers, booleans, and string lists. Nulls, nested objects, non-string lists,
// keys beginning with $, and the reserved document-content key are rejected
// before embedding or upsert. Remove an absent metadata key instead of null.
//
// Operation limits. Search applies the adapter's [MaxTopK] result limit to
// both native and locally ranked queries. One upsert carries at most
// [MaxVectorsPerUpsert] records; Index splits larger batches. Pinecone caps an
// upsert request at 2 MB, which a record count cannot predict, so that limit
// surfaces as a provider error.
//
// Lifecycle. The store implements [vectorstore.Closer] because it creates a
// resource of its own: construction opens an index connection through
// Client.Index, and Close releases that connection rather than the caller's
// client. The client stays the caller's to close.
//
// See https://docs.pinecone.io/ for the full API surface.
package pinecone
