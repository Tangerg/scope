// Package azureaisearch stores precomputed vectors and provides native vector
// and hybrid ranking over the Azure AI Search REST API. The host provisions the
// index, HTTP client, retries and credentials. Scope never creates the index.
//
// The index must have four retrievable fields: a sortable/filterable Edm.String
// key without normalization; a searchable content string; a searchable, stored
// Collection(Edm.Single) named Space.StorageKey(); and a plain unindexed string
// holding the complete Core metadata JSON, including null and precise numbers.
// The vector field's native profile defines dimension and metric. A different
// space needs a different index even if widths match. NewStore reads this schema;
// operations recheck it, without fencing concurrent administrative index changes.
//
// Upsert validates IDs, text-only documents, float32 coordinates and the size
// of all requests before publication. Each native upload replaces the whole
// record; batches contain at most 1000 actions and 16 MiB. Responses require
// per-key acknowledgments. A WriteError distinguishes acknowledgments,
// rejections, unknown outcomes and groups not attempted. Search visibility is
// asynchronous; there is no transaction across documents.
//
// Query requires a same-space vector. Hybrid adds LexicalText for native RRF;
// semantic and hybrid ranking validate every returned record before thresholds,
// order by raw score and break ties by ID. TopK is limited to 1000. Native
// cosine scores are inverted to similarity; other scores clamp to [0,1].
// Core predicates return ErrUnsupportedFilter before any source read. Scan is
// an explicit alternative with independent positive maxima for records, observed
// JSON row bytes and time. Nonmatches count; errors return no partial rows.
// Per-request HTTP framing is bounded separately by MaxResponseBytes.
// Keyset scans and continuations are not snapshots. The host must quiesce
// concurrent writes when complete stable membership is required. Continuation
// parameters never control the destination of credentials.
//
// DeleteIDs deduplicates explicit keys, including unknown keys. The API offers
// no predicate CAS; consequently there is no delete-by-scan operation.
// Live tests require SCOPE_AZURE_SEARCH_ENDPOINT and SCOPE_AZURE_SEARCH_API_KEY
// and create and drop their own index. See https://learn.microsoft.com/en-us/azure/search/vector-search-ranking.
// Native contracts: https://learn.microsoft.com/en-us/rest/api/searchservice/documents/.
package azureaisearch
