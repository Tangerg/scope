// Package bedrockkb consumes Amazon Bedrock Knowledge Bases' native Retrieve
// REST API as a semantic and hybrid vectorstore.Searcher. Bedrock owns ingestion,
// chunking, embeddings and persistence; this adapter exposes no mutation APIs.
//
// The host provisions the knowledge base and supplies its runtime service origin
// and an authenticated HTTP client through StoreConfig. AWS SigV4 signing,
// credential refresh, retries, timeouts and transport lifecycle stay with that
// client. NewStore validates configuration without network I/O; an unknown
// knowledge base or unsupported native search policy fails on Search. The
// constructor keeps the family's context-first signature.
//
// Metadata is decoded directly from native JSON into Core metadata.Map. It never
// passes through the SDK's generic document decoder, which converts JSON numbers
// to float64 and can lose integer and decimal precision. Missing or null metadata
// stays nil; an explicit empty object stays non-nil. Scope cannot recover facts
// that the data source or Bedrock has already rounded before emitting JSON.
//
// Core predicates are unsupported and fail with errors.ErrUnsupported before any
// HTTP request. Retrieve cannot enumerate the complete knowledge base for Core's
// predicate validation, and native stringContains and negated comparisons do not
// have Core's type and missing-value semantics. The adapter has no private filter
// compiler or post-TopK filtering path. Optional native implicit filtering remains
// an explicit host policy in StoreConfig, with Bedrock owning its interpretation.
// Construction snapshots native settings; later changes to the supplied SDK
// values cannot advance the Store's retrieval policy.
//
// SearchOptions.TopK owns both numberOfResults and numberOfRerankedResults. The
// native limit is 100 total requested chunks, not a page size; larger limits fail
// with vectorstore.ErrInvalidOptions before I/O. Search follows native nextToken
// values to completion, including short or empty advancing pages, and validates
// every returned chunk before applying MinScore and TopK. A repeated token or an
// invalid later chunk returns an error and no partial response.
//
// Only native TEXT chunks with finite scores and stable source identity fit this
// contract. Document IDs come from documentId when present, otherwise from the
// canonical active source location; an SQL query alone is not an identity. Several
// chunks can share a source document ID and are retained as distinct matches.
// Native raw scores determine rank before clamping onto Core's [0,1] relevance
// scale. Bedrock does not document a scale conversion, so clamping cannot make
// these scores comparable across knowledge bases or ranking policies.
//
// Migration is a replacement of the API: remove Client/RetrieveClient and provide
// the host's signed HTTPClient and Endpoint. Replace RerankingConfiguration with
// RerankingModelConfiguration and optional RerankingMetadataConfiguration; remove
// its independent result count. Remove Core filters or select a store that can
// honor them. Request at most 100 results. No SDK response path, filter adapter,
// legacy constructor or dual decoding path remains. No persisted Scope schema is
// involved, so existing knowledge-base data need not be rewritten by Scope.
//
// Native integration requires AWS_REGION, SCOPE_BEDROCK_KB_ENDPOINT,
// SCOPE_BEDROCK_KB_ID, and credentials available to the AWS SDK's default chain.
// The knowledge base must already contain ingested text documents. The test only
// retrieves data and creates no cloud resources; missing configuration fails.
//
// See https://docs.aws.amazon.com/bedrock/latest/APIReference/API_agent-runtime_Retrieve.html
// and https://docs.aws.amazon.com/bedrock/latest/userguide/kb-test-retrieve.html.
package bedrockkb
