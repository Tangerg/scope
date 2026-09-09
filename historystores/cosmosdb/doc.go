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
// write, far from the wiring that chose the container.
//
// One Write is one transactional batch. Because every message in a Write
// carries the same conversation id, the batch satisfies Cosmos's rule that
// "all operations within a TransactionalBatch must operate on items within the
// same partition key", and so earns its guarantee: "if any operation fails,
// the entire transaction is rolled back". A rolled-back batch still arrives as
// a successful HTTP call, so Write reads the batch's own outcome rather than
// the call's, and names the operation Cosmos faulted — every other one carries
// 424 to say it was rolled back rather than that it failed. Cosmos caps a
// batch at [MaxMessagesPerWrite] operations and Write refuses a larger one,
// because splitting it across batches would trade that guarantee for a
// partial conversation nothing reported.
//
// Reads issue a single-partition query and order the materialized
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
