// Package vectorstore defines the raw, space-bound database capabilities.
// [Upserter] writes prepared records and [Querier] ranks prepared coordinates.
// Semantic text encoding and ingestion belong to rag/vector and rag/index.
// [IDDeleter] and [FilterDeleter] are independent optional operations.
// Unsupported native predicates must fail without enumerating source documents.
// [ScanBudget] grants an explicit, finite exception on backends that support it.
// Scan enumerates documents under [ScanRequest]; QueryByScan ranks a [Query]
// under an explicit [ScanBudget]. Consumers declare the narrow capability they use.
package vectorstore
