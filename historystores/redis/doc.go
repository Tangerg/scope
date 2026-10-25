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
// Enumeration requires an explicit complete keyspace traversal because a
// decorated client's routing behavior cannot establish its topology. Each node
// is scanned to completion with its own cursor. Concurrent writes can still
// change membership during SCAN; complete coverage is not a linearizable snapshot.
// Invalid stored conversation IDs and unavailable nodes fail without a partial list.
//
// For a cluster, set StoreConfig.ScanNodes to client.ForEachMaster. For a single
// node, supply a traversal that visits that client:
//
//	client := goredis.NewClient(&goredis.Options{...})
//	store, _ := redis.NewStore(ctx, redis.StoreConfig{
//		Client: client,
//		ScanNodes: func(ctx context.Context, visit func(context.Context, *goredis.Client) error) error {
//			return visit(ctx, client)
//		},
//	})
package redis
