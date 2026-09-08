// Package vectorstore defines provider-neutral semantic indexing and search.
// Six independent interfaces split the surface by capability:
//
//   - [Indexer] indexes documents.
//   - [Searcher] finds similar documents by query + metadata filter.
//   - [IDDeleter] removes documents by identifier.
//   - [FilterDeleter] removes documents matching a metadata filter.
//   - [Closer] releases the resources a store created for itself.
//   - [Batcher] supplies an order-preserving ingestion partition policy.
//
// There is deliberately no aggregate Store interface: consumers depend only
// on the capabilities they call, and providers implement only what they can
// support. Batching remains an injected capability rather than a framework
// dependency. [IndexRequest] owns the shared validation and batching boundary
// before provider I/O. [SearchRequest] validates search input, while
// [SearchResponse] validates ranked provider output against that request.
//
// Indexed documents have a caller-assigned, non-empty ID and non-empty text.
// Providers preserve both values so every successful [SearchResult] is immediately
// usable by retrieval pipelines. Providers never generate IDs, and a
// vector-index-plus-external-document-store architecture must hydrate results
// explicitly outside these capabilities rather than return partial Documents.
// Provider distances and similarities are converted to the common [Score]
// contract with the ScoreFrom functions in this package.
//
// Metadata filtering uses the filter mini-language: build predicates with
// typed constructors or parse them from text with filter.Parse. See
// [github.com/Tangerg/scope/core/vectorstore/filter]. SearchOptions JSON stores
// the canonical DSL string so serializing a request preserves its predicate.
//
// Quick start:
//
//	expr, _ := filter.Parse(`category == 'tech' AND year >= 2020`)
//	req := &vectorstore.SearchRequest{
//		Query: "attention",
//		Options: vectorstore.SearchOptions{TopK: 5, MinScore: 0.7, Filter: expr},
//	}
//	response, err := searcher.Search(ctx, req)
package vectorstore
