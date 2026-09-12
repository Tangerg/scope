// Package history defines provider-neutral conversation history contracts
// and model middleware.
//
// Reader, Writer, Clearer, and Store use core/chat protocol values directly.
// WindowStore is an explicit read-side retention decorator. It merges system
// messages and retains only complete user-led turns, so assistant Tool calls,
// Tool results, reasoning, and final text cannot be split at the read boundary.
// Optional cross-conversation and replacement capabilities remain separate
// interfaces.
// The zero-value-ready reference implementation lives in core/history/inmemory.
//
// Conversation IDs are runtime scope carried with [WithConversationID], not
// serialized request metadata. Middleware binds that scope to model calls.
//
// Writes preserve message order within one call. Conversation listing is an
// optional capability and returns unique IDs in lexical order. Concurrent
// writes and writes through distinct Store instances have no common ordering
// guarantee unless a backend documents one. Writer returns WriteOutcome even
// on failure; errors never silently mean that no messages were stored. A
// CommitError identifies history failure after model completion and preserves
// the attempted messages and write outcome.
//
// The Host owns conversation turn ordering. Concurrent-safe store methods do
// not isolate the whole Read/model/Write sequence; multi-instance hosts must
// coordinate that sequence across instances if they require serial turns.
//
// Persistent backends live in independent leaf modules so database drivers do
// not enter Core:
//
//	historystores/postgres/  — PostgreSQL (pgx + JSONB)
//	historystores/redis/     — Redis (RPUSH / LRANGE lists)
//	historystores/mongodb/   — MongoDB (document per message)
//	historystores/cassandra/ — Cassandra (TIMEUUID clustering key)
//	historystores/neo4j/     — Neo4j (node per message)
//	historystores/cosmosdb/  — Azure Cosmos DB (NoSQL API)
//
// Every backend reads and writes only the current core/chat tagged JSON wire.
package history
