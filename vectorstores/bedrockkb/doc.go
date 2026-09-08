// Package bedrockkb wraps AWS Bedrock Knowledge Bases as a semantic and hybrid searcher.
// Bedrock Knowledge Base is a managed RAG
// service — embedding, chunking, and persistence are all handled
// behind the API; scope only consumes the runtime Retrieve surface.
//
// Requirements: an AWS account with Bedrock Knowledge Bases enabled,
// a provisioned knowledge base wired to a data source (S3, Confluence,
// SharePoint, Salesforce, etc.), and an aws-sdk-go-v2
// bedrockagentruntime client.
//
// Document lifecycle. Bedrock ingests via the configured data source
// + StartIngestionJob — there's no runtime upsert / delete. The store exposes
// no fake mutation methods. Manage documents via the data source instead
// (StartIngestionJob through the bedrockagent control plane).
//
// Retrieve uses Bedrock's runtime Retrieve API with the configured
// [types.KnowledgeBaseVectorSearchConfiguration] — NumberOfResults
// is populated from [vectorstore.SearchOptions.TopK], and
// [vectorstore.SearchOptions.Mode] maps directly to Bedrock's semantic or
// hybrid search type. Provider-specific reranking and implicit filtering stay
// in [StoreConfig].
//
// Result completeness. NumberOfResults caps at 100 and Bedrock answers with a
// continuation token whenever more results exist than fit in one response, so a
// single call is not the whole answer even when it asks for fewer than the cap.
// Search follows the token until TopK results are collected or the knowledge
// base is exhausted; TopK above 100 is served by paging rather than rejected.
//
// Filter visitor produces [types.RetrievalFilter] — Bedrock's typed
// filter shape (Equals / NotEquals / GreaterThan / LessThan /
// GreaterThanOrEquals / LessThanOrEquals / StringContains / In /
// NotIn / AndAll / OrAll / etc.).
//
// Identifiers. Bedrock retrieval results don't expose stable per-row
// ids; the store uses `DocumentId` or the result's `Location` (e.g. the S3 URI
// of the source object). A result without either stable identity is rejected.
//
// Null tests are refused. Bedrock's RetrievalFilter offers equals, notEquals,
// the four ordering members, in, notIn, startsWith, listContains and
// stringContains, none of which asks whether a key is present, so an IS NULL
// filter fails rather than being approximated.
//
// See https://docs.aws.amazon.com/bedrock/latest/userguide/knowledge-base.html.
package bedrockkb
