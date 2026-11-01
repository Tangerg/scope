// Package bedrockkb retrieves ranked text evidence from Amazon Bedrock Knowledge Bases.
// Bind a provisioned knowledge base, native search/reranking policy, optional exact
// metadata scope and host-owned authenticated HTTP transport with New. Retrieve
// forwards only the query text; returned scores are native, query-relative values.
// Pagination is bounded by PageLimit and a cumulative MaxResponseBytes. An incomplete
// ranking or a result outside the bound scope fails the call. Evidence identities
// exclude scores and preserve exact metadata, native location and source document ID.
// Bedrock owns ingestion, embeddings and storage.
package bedrockkb
