// Package redis stores prepared vectors in host-owned Redis HASHes and performs
// native KNN search. NewStore and operations inspect the concrete RESP3 FT.INFO
// definition: one unfiltered HASH prefix ending with SpaceID.StorageKey()+":",
// one FLOAT32 embedding VECTOR (COSINE, L2 or IP), optional content TEXT and
// optional metadata_terms TAG with comma separator. The Host provisions the
// index and owns the client, topology, credentials and timeouts.
//
// Upsert prepares every row before writing. Each HASH contains content, vector
// bytes, canonical metadata_json, and a derived metadata_terms projection. Lua
// replaces one HASH atomically. WriteBatchSize bounds the nontransactional
// pipeline; vectorstore.WriteError exposes confirmed, uncertain and unattempted
// IDs. Search index visibility can lag a confirmed write.
//
// Query has no enumeration path. It ranks with FT.SEARCH; its native predicate
// subset is scalar equality on a top-level string when the TAG index exists.
// Field names and exact string values are encoded as lowercase hexadecimal TAG
// tokens. Response warnings, incomplete replies, damaged records and predicates
// outside the supported subset are errors.
//
// QueryByScan and DeleteMatching explicitly accept a time, record and byte grant.
// SCAN COUNT is only a hint: each master bounds key-page output in Lua. Each
// distinct key is charged before its HASH payload is fetched; bytes count the
// key plus the four field names and values, excluding protocol framing. A second
// atomic size check rejects concurrent growth; vanished keys are charged for
// their names. The host binds ScanNodes to the complete authoritative keyspace;
// ClusterClient.ForEachMaster supports decorated clients without type discovery.
// Ring.ForEachShard is insufficient because it can skip unavailable shards. A slow
// scan may hit its deadline and has no snapshot isolation. No partial search
// result is returned. DeleteMatching finishes selection then compares current
// canonical metadata before deleting each record; earlier deletions may survive
// a later failure. DeleteIDs accepts explicit identities and uses single-key DEL.
//
// Integration fixtures require SCOPE_REDIS_ADDR on an isolated Redis Search
// instance. They create disposable native indexes and remove them afterwards.
package redis
