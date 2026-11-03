// Package cosmosdb stores history in an Azure Cosmos DB for NoSQL container.
//
// The container must use /conversation_id as its hash partition key and its
// account must support Strong consistency. NewStore verifies both conditions
// through read-only SDK requests. Every read, query, and transaction requests
// Strong consistency; the service rejects accounts that cannot supply it.
// Multi-region writes with asynchronous conflict resolution cannot establish
// this Store's single-owner append and permanent-closure guarantees.
//
// A permanent conversation document owns closure and append order. Append
// compares its ETag and creates one immutable batch item in the same partition
// transaction. Batch owns encoding and digest; the backend never rehashes it.
// The same append identity and digest receives the original receipt, while
// changed content conflicts. Failed transport acknowledgment remains unknown;
// only a confirmed conditional rollback is retried internally. The service's
// transactional payload limit applies to the complete append; a batch is never
// split across commits. Receipt and gate items explicitly disable TTL.
//
// Clear first closes the permanent gate atomically, immediately hiding all
// messages and rejecting every append, including old retries. It then removes
// payload items while retaining that gate. If cleanup fails, Clear returns the
// error and a later Clear resumes deletion; closure is never undone. This keeps
// closure atomic even when a conversation exceeds Cosmos's transaction limit.
// Reads pin the initial sequence and recheck closure, so concurrent appends or
// cleanup cannot expose a partial batch or a partially cleared conversation.
// The Store does not close its caller-owned ContainerClient.
package cosmosdb
