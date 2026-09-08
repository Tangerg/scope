// Package couchbase exposes Couchbase Search Service vectors
// through the Core vector-store capability interfaces. Documents are upserted as JSON
// (`{id, content, metadata, embedding}`); queries use SQL++ (N1QL)
// with an embedded `SEARCH(...)` k-NN clause that targets a
// Couchbase FTS index.
//
// Requirements: Couchbase Server 7.6+ — that's when the Search
// Service learned to index dense vectors and answer KNN queries.
// The store talks to the cluster over gocb v2.
//
// Similarity functions: [SimilarityCosine] / [SimilarityL2Norm] /
// [SimilarityDotProduct]. Default is dot product (matches the
// the framework defaults); pick cosine if your embedder isn't normalised.
//
// Index optimization knobs: [OptimizeRecall] (default),
// [OptimizeLatency], [OptimizeMemory] — they hint Couchbase how to
// trade recall against latency / memory at index build time.
//
// Filter visitor produces SQL++ predicates under the `metadata.*`
// path; each segment is backtick-quoted so reserved chars / keywords
// pass through. Vectors are inlined into the SQL as JSON arrays —
// gocb's standard parameter binding doesn't yet carry a typed vector
// shape, but the value is a plain number array so it's safe.
//
// Schema. The store provisions an FTS index of type `vectorSearch`
// under [StoreConfig.InitializeSchema] = true, mirroring the JSON
// template the framework ships.
//
// Statement results. gocb reports a failure raised while a query result streams
// through Err and Close, not from the initial call, so every statement the
// store runs — searches and filtered deletion alike — goes through one owner
// that iterates, checks the stream, and closes the result. A statement that
// failed mid-stream is an error even though its first response succeeded.
//
// See https://docs.couchbase.com/server/current/vector-search/
// vector-search.html for the official reference.
package couchbase
