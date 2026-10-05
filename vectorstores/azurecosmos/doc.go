// Package azurecosmos implements Core indexing, semantic search and conditional
// filter deletion within one Azure Cosmos DB for NoSQL logical partition. The
// host owns the native SDK client, credentials, retries, transport lifecycle,
// account, database and container provisioning.
//
// NewStore reads the container's identity, Hash partition-key path
// /partition_key and /embedding vector policy. Float32 dimensions in [1,4096]
// and cosine, dotproduct or euclidean come exclusively from that native policy.
// Queries use VectorDistance without distance or data-type overrides. Native
// vector policy and container lifecycle must remain stable while the store is
// used. Native Hash partition versions determine the partition-key byte budget.
//
// Scope items have exactly id, partition_key, content, metadata and embedding,
// plus Cosmos system properties. Metadata is one string containing the complete
// Core JSON object or null. Nil, empty objects, nested values and arbitrary JSON
// numbers survive native numeric limits without a second filterable projection.
// Replacing an item replaces its complete text, metadata and vector. Media is
// unsupported. Current reads are closed: legacy object metadata, extra user
// properties, absent fields, invalid vectors or absent native ETags fail.
//
// Index validates all documents and native item budgets before embedding, then
// prepares every batch, narrows vectors and verifies native dimensions before
// the first upsert. Each actual encoded item must fit the native 2 MiB limit.
// A later model or local validation failure publishes no items. Native failures
// can leave earlier upserts committed; this capability is not a transaction over
// the complete request and adds no retry or compensating deletion.
//
// Construction and Search validate every item in the bound partition. Search
// evaluates Core predicates using filter.Match before embedding or TopK, retains
// only selected IDs and ETags, and projects IDs into native parameterized IN
// clauses in groups of at most 100. Unfiltered search uses the same native vector
// ranking operation. No provider predicate compiler or local distance algorithm
// exists. Every ranked page is validated before MinScore or TopK is applied.
// Raw native similarity orders cosine and dot product; raw distance orders
// Euclidean results. Bytewise ID resolves ties before Core score projection.
//
// Repeated IDs or continuation tokens, corrupt later items, invalid scores,
// changed predicate membership and native errors return no partial response.
// Empty pages with advancing continuation tokens are supported. Scanning reads
// the whole partition and consumes native RUs. Scan and ranking calls do not
// share a snapshot; callers requiring stable membership must coordinate writes.
//
// DeleteWhere scans and validates the complete partition before its first
// deletion. Each selected item is deleted with its observed native ETag in
// If-Match. A changed or removed item fails explicitly; no unconditional delete,
// retry, silent skip or metadata arbitration follows. Multiple deletions are
// not atomic: a later native conflict can leave earlier deletions committed.
//
// Migration removes DistanceFunction, its public enum and all field-name
// settings. Provision /partition_key and the native /embedding float32 policy,
// then rebuild source documents into the current JSON-string metadata schema.
// Legacy records are rejected; there is no dual read, schema version, migration
// registry or compatibility adapter. SDK and store construction are shown in the
// checked ExampleNewStore.
//
// Native integration requires SCOPE_COSMOS_ENDPOINT, SCOPE_COSMOS_KEY and
// SCOPE_COSMOS_DATABASE. It creates unique test containers in that database and
// deletes only those containers. Missing setup fails. The account must support
// vector search and container creation with dedicated throughput.
//
// See https://learn.microsoft.com/en-us/azure/cosmos-db/vector-search and
// https://learn.microsoft.com/en-us/azure/cosmos-db/database-transactions-optimistic-concurrency.
package azurecosmos
