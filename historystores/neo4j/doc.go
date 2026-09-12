// Package neo4j is a history Store backed by Neo4j via the official
// Go driver (v5).
//
// Storage model:
//
//	(:ChatMessage {
//	    conversation_id: "u-42",
//	    seq:             <int64 nanos>,
//	    message:         "<json>",
//	    created_at:      <datetime>
//	})
//
// A composite index on (`conversation_id`, `seq`) is created by
// InitializeSchema=true so reads stream in insertion order without a
// full collection scan. A store-local sequence generator reserves one
// contiguous range per Write and remains monotonic across local clock
// regression. Concurrent calls and writes from distinct Store instances have
// no defined relative order.
//
// Write atomicity. One Write is one UNWIND ... CREATE statement, so it applies
// whole or not at all. A driver error does not prove rollback: losing the
// acknowledgment may hide a completed commit. WriteOutcome reports uncertainty
// on execution errors; callers must reconcile before resubmitting the batch.
//
// Example:
//
//	drv, _ := neo4j.NewDriverWithContext("neo4j://...", auth)
//	defer drv.Close(ctx)
//	store, _ := neo4jstore.NewStore(ctx, neo4jstore.StoreConfig{
//	    Driver:           drv,
//	    Database:         "neo4j",
//	    InitializeSchema: true,
//	})
package neo4j
