// Package redis is a history Store backed by Redis via go-redis.
//
// Each conversation maps to a Redis list keyed by
// `<KeyPrefix><conversationID>` (default prefix `chat:history:`).
// Messages are RPUSH'd as canonical [chat.Message] JSON, so a
// LRANGE 0 -1 preserves list order. One Write is one RPUSH carrying every
// message, so it applies whole or not at all and a failed one leaves no prefix
// behind. When TTL is configured, that append and the expiry refresh execute
// in one Redis transaction.
//
// Enumeration spans every node. SCAN carries no key, so go-redis routes it to
// one shard chosen by its picker, and a cursor belongs to the node that issued
// it: against a cluster or ring, a single scan loop would report an arbitrary
// subset of conversations as the complete set while feeding one node's cursor
// to another. Conversations drives each master or shard through its own cursor
// and merges the results. Only go-redis's own
// [goredis.ClusterClient] and [goredis.Ring] can be recognized: a wrapper that
// hides a multi-node client behind [goredis.UniversalClient] is scanned as a
// single client, because nothing here can unwrap it.
//
// Example:
//
//	client := goredis.NewUniversalClient(&goredis.UniversalOptions{...})
//	store, _ := redis.NewStore(ctx, redis.StoreConfig{Client: client})
package redis
