// Package redis exposes Redis Stack's RediSearch module
// through the Core vector-store capability interfaces. Documents are stored as Redis HASHes keyed at
// `<KeyPrefix><id>`; an FT.CREATE-defined index registers the
// vector field plus any pre-declared metadata fields.
//
// Requirements: Redis Stack (or Redis OSS 8.0+ with the search
// module) — RediSearch is mandatory. RedisJSON is NOT required;
// the store deliberately uses HASH storage to keep the dependency
// surface minimal.
//
// Distance metrics: [DistanceCosine] / [DistanceL2] / [DistanceIP].
// Vector index algorithm: [AlgorithmHNSW] (default) / [AlgorithmFlat].
//
// Metadata model. Every filterable metadata key MUST be declared in
// [StoreConfig.MetadataFields] up-front with its RediSearch type —
// [FieldTag] (exact match), [FieldNumeric] (range queries), or
// [FieldText] (full-text). Filters against undeclared fields fail
// fast via [ErrUnknownMetadataField] (rather than reaching Redis
// and silently producing zero hits).
//
// A document's metadata of record is the JSON in
// [StoreConfig.MetadataJSONField], which is not part of the index schema. The
// declared fields are the index projection of that record: RediSearch indexes a
// HASH field's text as its declared type, so a declared field has to hold the
// value in the form the index expects and cannot also carry the value's type.
// Reading metadata back from those fields turned a number into a float64 and
// everything else into a string, and an undeclared key had no field to read at
// all, so a search returned a document that differed from the one that was
// written. Reading the record instead makes the round trip exact and keeps
// undeclared keys, and the projection no longer has to be reversible.
//
// Query path. The filter visitor emits RediSearch syntax — TAG
// `@f:{v}`, NUMERIC `@f:[low high]`, TEXT `@f:(v)`. Vector retrieval
// runs FT.SEARCH with the hybrid syntax
// `(<filter>)=>[KNN K @embedding $vec AS distance]`, passing the
// binary FLOAT32 little-endian vector through PARAMS.
//
// Result completeness. RediSearch bounds every query with TIMEOUT and its
// default ON_TIMEOUT policy answers successfully with the hits gathered so far,
// reporting the truncation as a warning. Search and filtered deletion reject a
// warned result, and deletion re-queries until a page comes back empty rather
// than reading a short page as an exhausted match set.
//
// Null tests are refused. A RediSearch index has no predicate for a field that
// was never written — an unindexed field is simply absent from the inverted
// index — so an IS NULL filter fails rather than being approximated.
//
// Existing index. InitializeSchema creates the index when it is missing, and
// verifies it when it is not. Existence was previously taken for agreement:
// search converts RediSearch's distance into a Score using the configured
// metric, so an index built with L2 while the config says COSINE returned
// scores that were wrong rather than absent — nothing failed, the ranking was
// silently mis-scaled. FT.INFO now supplies the vector attribute's metric and
// dimension, and a mismatch fails construction with [ErrIncompatibleIndex],
// where the misconfiguration is.
//
// Field names. Every configured field name is written into the RediSearch
// query language as text — FT.CREATE declares it and a filter emits it as
// `@name` — and RediSearch cannot quote a field name, so construction
// requires each to be a dot-separated path of plain identifiers. The dots
// are allowed because a RediSearch schema is flat: a nested metadata key is
// declared as a dotted field name, and that is the only way to filter one.
// A filter can still only reference a declared field, so a key chosen at
// query time never reaches the query language unchecked.
//
// See https://redis.io/docs/latest/develop/interact/search-and-query/
// for the RediSearch reference.
package redis
