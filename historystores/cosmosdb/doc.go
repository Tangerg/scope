// Package cosmosdb is a history Store backed by Azure Cosmos DB
// (NoSQL API) via the official Azure SDK.
//
// Each message is stored as a document with a collision-resistant random ID:
//
//	{
//	    "id":              "4ZK3VZQF...",
//	    "conversation_id": "u-42",
//	    "seq":             "1716210000000123456",
//	    "message":         "<json>",
//	    "created_at":      "2026-05-20T08:00:00Z"
//	}
//
// `conversation_id` is the partition key, set when provisioning the
// container: every read and delete is scoped to one partition by it, so
// [NewStore] reads the container and refuses one partitioned elsewhere with
// [ErrIncompatibleContainer] rather than letting Cosmos reject the first
// write, far from the wiring that chose the container. Reads issue a single-partition query and order the materialized
// documents by (`seq`, `id`) without requiring a Cosmos composite index. `seq`
// is a fixed-width decimal string so lexicographic ordering is numeric and
// Cosmos' floating-point JSON number representation cannot lose nanosecond
// precision. A store-local sequence generator reserves one contiguous range
// per Write and remains monotonic across local clock regression. Concurrent
// calls and writes from distinct Store instances have no defined relative
// order.
//
// Example:
//
//	cosmos, _ := azcosmos.NewClient(endpoint, cred, nil)
//	container, _ := cosmos.NewContainer("scope", "chat_history")
//	store, _ := cosmosdb.NewStore(ctx, cosmosdb.StoreConfig{Container: container})
package cosmosdb
