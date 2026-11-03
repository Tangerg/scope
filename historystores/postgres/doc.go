// Package postgres stores history through a caller-owned pgx pool.
//
// Each [history.Batch] occupies one row containing its stable append ID, digest,
// and canonical JSON bytes in a BYTEA column. JSONB cannot preserve the complete
// Core wire vocabulary. The conversation table permanently owns closure and
// serializes appends with SELECT FOR UPDATE; the receipt and payload commit in
// the same transaction. Repeating the same identity and digest returns its
// receipt, while changed content returns [history.ErrAppendConflict].
//
// Clear takes the same conversation lock and commits permanent closure before
// removing payloads. Closure immediately hides every message. Cleanup failures
// leave the gate closed; another Clear resumes removal. Receipt digests and
// closure remain durable.
// Even a retry of a formerly accepted batch fails after closure. A failed commit
// acknowledgment returns no receipt; retrying the original Batch resolves the
// outcome without appending twice.
//
// InitializeSchema creates the append table, conversation table, and read index.
// Existing deployments must provision this current schema; there are no legacy
// table fallbacks. A Store never closes its caller's pool.
package postgres
