// Package mongodb is a history Store backed by MongoDB via the
// official mongo-driver v2.
//
// Each message is a document in the configured collection:
//
//	{
//	    "_id":             ObjectId(...),     // assigned by the driver
//	    "conversation_id": "u-42",
//	    "seq":             1716210000000123456,
//	    "message":         "<json>",          // canonical chat.Message wire shape
//	    "created_at":      ISODate(...),
//	}
//
// Documents are read by (`seq`, `_id`). A store-local sequence generator
// reserves one contiguous range per Write and remains monotonic across local
// clock regression. Concurrent calls and writes from distinct Store instances
// have no defined relative order.
//
// Enumeration groups rather than using the distinct command, even though
// listing conversations is exactly a distinct query. MongoDB documents three
// limits on distinct that all share one remedy: on a sharded cluster it "may
// return orphaned documents", which here would be conversation ids the owning
// shard no longer holds; its single result document is bounded by the maximum
// BSON size, which would cap how many conversations a store may hold; and on a
// sharded collection inside a transaction it is unavailable. For each, MongoDB
// says to "use the aggregation pipeline with the $group stage instead", which
// also streams through a cursor.
//
// Write acknowledgment. The collection must use an acknowledged write concern.
// Under w: 0 MongoDB sends no reply, so the driver reports success for messages
// it never learned the fate of; Write and Clear reject that result instead of
// passing the silence on as a stored conversation.
//
// Example:
//
//	col := client.Database("scope").Collection("chat_history")
//	store, _ := mongodb.NewStore(ctx, mongodb.StoreConfig{
//	    Collection:       col,
//	    InitializeSchema: true, // create the conversation_id index
//	})
package mongodb
