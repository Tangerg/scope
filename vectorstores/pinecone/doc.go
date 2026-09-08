// Package pinecone exposes Pinecone through the Core vector-store capability interfaces.
// Documents are stored as vectors in a Pinecone index
// (`{id, values, metadata}`); retrieval runs the index's similarity
// query.
//
// Requirements: a Pinecone account and an existing index (created
// via the Pinecone console or control-plane API — Pinecone does not
// allow lazy index creation from the data plane). The store uses
// the official pinecone-io/go-pinecone v4 client.
//
// Vector similarity. Pinecone configures cosine / dotproduct /
// euclidean at index-creation time; the store reads but does not
// override.
//
// Filter visitor produces Pinecone's metadata-filter syntax —
// `{"author": {"$eq": "Alice"}}`, `{"$and": [...]}`,
// `{"$in": [...]}`. The result feeds the `Filter` field of the
// query request. Pinecone has no native LIKE / regex; the visitor
// rejects [filter.OpLike] expressions explicitly.
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
// Filtered deletion is a pod-based index capability. Serverless and starter
// indexes reject a metadata filter, and that rejection surfaces as an error
// instead of an empty match set; compose deletion from DeleteIDs there.
//
// Null tests map to $exists. Pinecone metadata holds strings, numbers,
// booleans and string lists, so a key is either present with a value or
// absent and there is no stored null — which makes $exists: false exactly the
// filter AST's IS NULL, and $exists: true its negation.
//
// Operation limits. A query returns at most [MaxTopK] results and one upsert
// carries at most [MaxVectorsPerUpsert] records. Index splits a larger batch
// rather than sending a request certain to be rejected; Search refuses a
// larger TopK locally, because that one cannot be split. Pinecone also caps an
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
