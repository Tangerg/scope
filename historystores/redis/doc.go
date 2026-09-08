// Package redis is a history Store backed by Redis via go-redis.
//
// Each conversation maps to a Redis list keyed by
// `<KeyPrefix><conversationID>` (default prefix `chat:history:`).
// Messages are RPUSH'd as canonical [chat.Message] JSON, so a
// LRANGE 0 -1 preserves list order. When TTL is configured, append and expiry
// refresh execute in one Redis transaction.
//
// Enumeration spans every node. SCAN carries no key, so go-redis sends it to
// one shard chosen by its picker; against a cluster or ring, a single scan loop
// therefore reported an arbitrary subset of conversations as the complete set,
// and fed one node's cursor to another. Conversations now drives each master or
// shard through its own cursor and merges the results. Only go-redis's own
// [goredis.ClusterClient] and [goredis.Ring] can be recognized: a wrapper that
// hides a multi-node client behind [goredis.UniversalClient] is scanned as a
// single client, because nothing here can unwrap it.
//
// Example:
//
//	client := goredis.NewUniversalClient(&goredis.UniversalOptions{...})
//	store, _ := redis.NewStore(ctx, redis.StoreConfig{Client: client})
package redis
