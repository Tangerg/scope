// Package mongodb persists immutable append batches through MongoDB transactions.
//
// A permanent conversation gate serializes appends and closure. Each append
// document holds the Batch owner's digest and complete canonical message bytes;
// a transaction publishes both together and allocates their stored order. All
// transactional operations use the collection's own client, snapshot reads,
// primary routing and majority commit acknowledgment. A deployment without
// transaction support fails instead of falling back to partial InsertMany.
//
// Clear commits permanent closure before a separate cleanup transaction.
// Read and listing lock the same gate before observing messages, so a stale
// snapshot or failed cleanup cannot reopen a closed conversation. Listing
// checks each gate in its own short transaction; it is not a global snapshot.
// The permanent tombstone replaces their receipts because all future appends
// under a closed conversation ID are rejected. Receipt records for an open
// conversation must not be deleted by TTL or an external retention policy.
// The host owns the native client and collection lifecycle.
package mongodb
