// Package redis is a history Store backed by Redis via go-redis.
//
// Each conversation maps to a Redis list keyed by
// `<KeyPrefix><conversationID>` (default prefix `chat:history:`).
// Messages are RPUSH'd as canonical [chat.Message] JSON, so a
// LRANGE 0 -1 preserves list order. One Write is one RPUSH carrying every
// message, so the append is atomic. A lost reply makes its outcome uncertain.
// When TTL is configured, append and expiry refresh execute in one transaction,
// whose command errors do not roll back successful commands. WriteOutcome
// retains a confirmed append even if expiry refresh fails.
//
// Enumeration scans each Redis Cluster master to completion and merges the
// results. A Ring supports Read, Write and Clear, but Conversations returns
// errors.ErrUnsupported: the SDK's ForEachShard silently skips down shards and
// exposes no complete current topology with which to verify coverage. Only
// concrete go-redis multi-node clients can be recognized; a wrapper hiding one
// behind [goredis.UniversalClient] must provide complete single-client SCAN
// semantics for enumeration.
//
// Example:
//
//	client := goredis.NewUniversalClient(&goredis.UniversalOptions{...})
//	store, _ := redis.NewStore(ctx, redis.StoreConfig{Client: client})
package redis
