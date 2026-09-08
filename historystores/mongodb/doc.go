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
