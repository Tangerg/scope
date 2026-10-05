// Package azureaisearch implements Core indexing, semantic and hybrid search,
// and explicit ID deletion over the Azure AI Search REST API.
//
// The host provisions one dedicated index and injects an authenticated HTTP
// client. Authentication, token refresh, transport timeouts, retries and client
// lifecycle belong to the host. Construction reads the actual index schema and
// validates existing records without calling the embedding model. The bound
// schema must remain unchanged for the lifetime of a Store.
//
// The current schema has exactly four retrievable fields, named by StoreConfig:
//
//   - ID: Edm.String, the sole key, filterable and sortable, without a normalizer.
//   - Content: searchable Edm.String for native lexical evidence.
//   - Vector: searchable, stored Collection(Edm.Single), with positive dimensions
//     and an explicit native vector profile, algorithm kind and metric.
//   - Metadata: unindexed Edm.String containing the complete Core metadata JSON.
//
// Core owns JSON values and predicate semantics, including precise numbers,
// nested keys, array indexing, Unicode LIKE, and null versus empty metadata.
// Index uploads complete records rather than merging individual metadata fields.
// Media is refused. IDs retain their original spelling; unsupported native key
// characters, leading underscores and keys over 1024 bytes are rejected without
// encoding aliases. All batches are embedded, narrowed to native float32,
// validated against the actual vector width and encoded within the 16 MiB
// request limit before the first write. Native writes use at most 1000 actions
// and require every per-document acknowledgment. Azure does not provide a
// transaction across actions: a later transport or document failure can leave
// earlier successful writes applied. Index visibility remains asynchronous.
//
// Search scans full stored records with ordered ID ranges until an empty page.
// Every record is decoded and checked by Core before embedding, ANN, TopK or
// threshold selection. Only Core-selected IDs enter the native search.in filter,
// with vectorFilterMode=preFilter. Semantic search supplies a vector; hybrid
// search adds lexical evidence from the content field and retains Azure's native
// fusion. ANN recall remains native and TopK cannot exceed 1000. Current returned
// records are validated and checked by Core again. Native continuation parameters
// advance paging while the configured endpoint retains credential authority.
// Repeated continuation requests, duplicate or unexpected IDs, invalid records
// and invalid native scores fail with no response.
//
// Native score order is retained before threshold selection, including when Core
// scores clamp to the same value. Cosine scores use Azure's documented transform
// to recover cosine similarity. Azure publishes no invertible transform for the
// other metrics or hybrid RRF, so those native scores clamp to Core's [0,1] range
// without an invented conversion. Equal native scores use bytewise ID order among
// the candidates returned by Azure.
//
// Reads do not form a collection snapshot. Concurrent writes can alter or remove
// candidates between scanning and querying. DeleteIDs expresses explicit key
// intent, ignores unknown keys and deduplicates repeated keys. DeleteWhere is not
// exposed: Azure's delete action ignores every supplied field except the key and
// offers no metadata CAS to protect a replacement after a predicate read.
//
// Breaking migration: rebuild a dedicated four-field index from original data;
// flat metadata indexes and merge-based records are not read or upgraded. Remove
// SimilarityMetric and APIKey configuration, inject authenticated HTTPClient,
// and use explicit ID deletion. There are no legacy schema branches or adapters.
//
// Native integration tests require SCOPE_AZURE_SEARCH_ENDPOINT and
// SCOPE_AZURE_SEARCH_API_KEY. They create and remove uniquely named indexes;
// missing environment fails instead of skipping. Default tests are offline.
//
// Native contracts: https://learn.microsoft.com/en-us/rest/api/searchservice/documents/
// and https://learn.microsoft.com/en-us/azure/search/vector-search-ranking.
package azureaisearch
