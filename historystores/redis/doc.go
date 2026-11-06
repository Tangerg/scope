// Package redis is a history Store backed by Redis 7 or later via go-redis.
//
// Each immutable history.Batch is one ordered list entry. A single Lua script
// checks the permanent closed gate and append receipt before adding the batch,
// recording its digest and refreshing the message-list TTL. Both keys share a
// conversation hash tag, so the same operation works on Redis Cluster. A lost
// reply is safe to retry under the same Batch identity, including SDK retries.
// Expired messages never remove receipts or make old batches eligible again.
// ACL checks precede append mutations because Lua errors do not roll back writes.
// Clear permanently closes the ID and removes its messages; reuse a new ID for
// a new conversation. A cleanup error can leave the ID closed; retry Clear to
// finish physical removal. Durable ledger keys require a persistence and eviction
// policy that does not discard them independently of application lifecycle.
//
// Enumeration requires explicit complete keyspace traversal: decorated command
// routing cannot establish topology. Each node's SCAN cursor runs to completion;
// concurrent mutations can affect membership. For a cluster bind ScanNodes to
// ClusterClient.ForEachMaster; for one node supply a callback visiting its Client.
//
// Native script and ACL contracts run with go test -tags=integration. They start
// isolated local redis-server processes; SCOPE_REDIS_SERVER selects the binary.
package redis
