// Package neo4j stores history through the official Neo4j driver.
//
// Each append node contains one Core Batch's canonical bytes and digest. A
// separate, unique conversation node owns closure and the last allocated append sequence.
// Append, Clear, and Read acquire a native write lock on that gate before reading
// dependent state; Neo4j's default read-committed isolation alone cannot prevent
// a read followed by a competing append or closure.
//
// NewStore verifies the required node uniqueness constraints even when the Host
// provisions them. InitializeSchema creates those constraints and the read
// index. Append executes as one native transaction. Repeated batch
// identities return the existing receipt only when the digest matches and the
// conversation is still open. Clear commits permanent closure before removing
// message payloads in a separate transaction. Cleanup failures leave the gate
// closed and a later Clear resumes removal. Retained digests do not authorize
// a retry after closure.
//
// Driver-managed retries reuse the same immutable Batch. A missing commit
// acknowledgment never produces a receipt, and a later identical Append safely
// reconciles it. Store does not own or close the supplied Driver.
package neo4j
