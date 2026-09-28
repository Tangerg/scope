// Package redis exposes Redis Stack's RediSearch module
// through the Core vector-store capability interfaces. Documents are stored as Redis HASHes keyed at
// `<KeyPrefix><id>`; an FT.CREATE-defined index registers the
// vector field plus any pre-declared metadata fields.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Requirements: Redis Stack (or Redis OSS 8.0+ with the search
// module) — RediSearch is mandatory. RedisJSON is NOT required;
// the store deliberately uses HASH storage to keep the dependency
// surface minimal.
//
// Distance metrics: [DistanceCosine] / [DistanceL2] / [DistanceIP].
// Vector index algorithm: [AlgorithmHNSW] (default) / [AlgorithmFlat].
//
// Metadata. The JSON in [StoreConfig.MetadataJSONField] is the exact document
// record. [StoreConfig.MetadataFields] configures optional native index fields;
// Core predicates are evaluated against the JSON record with filter.Match.
// Filtering scans every key in the configured namespace (every master on a
// Redis Cluster), before issuing bounded INKEYS vector queries. This costs
// O(N) metadata reads and key bookkeeping, plus the selected metadata bytes,
// and preserves scalar versus
// array membership, case, punctuation, whole-string LIKE and missing/null
// versus empty collections. No fixed vector candidate limit is used to decide
// metadata membership.
//
// Filtered deletion finishes enumeration before making changes, then compares
// the observed metadata bytes and deletes each key atomically with Lua. A key
// whose metadata changed after enumeration is retained. Enumeration and search
// are not a database snapshot: concurrent changes can be observed at different
// times, and index visibility remains subject to RediSearch's indexing state.
// Returned IDs and metadata are revalidated; a hit outside the selected IDs or
// no longer satisfying the predicate fails the entire search. Ring clients
// return errors.ErrUnsupported for filtered search and deletion because the
// SDK cannot enumerate every current shard, including unavailable shards.
//
// Vector retrieval uses FT.SEARCH with KNN, binary FLOAT32 vectors in PARAMS,
// and a bounded INKEYS list when filtered. Search rejects reported timeout
// warnings or unreadable hits rather than treating partial output as complete.
//
// Existing indexes must select exactly HASH keys with [StoreConfig.KeyPrefix],
// without another PREFIX or FILTER. FT.INFO also verifies the actual vector
// field, FLOAT32 representation, metric and declared dimension. Incompatibility
// returns [ErrIncompatibleIndex] at construction. Every returned key is checked
// against the namespace before its ID is exposed.
//
// Configured field identifiers must remain plain dotted identifiers for the
// native schema and vector query. Metadata selectors themselves are evaluated
// locally and can name undeclared keys without entering Redis query syntax.
//
// See https://redis.io/docs/latest/develop/interact/search-and-query/
// for the RediSearch reference.
package redis
