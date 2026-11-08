// Package managed publishes a compiled Scope Program as an MCP tool backed by
// an existing Runtime. It is optional; a single ordinary tool uses mcp.Register
// and needs no run, writer or continuation token.
//
// Register uses the Program's descriptor for name, description and input
// schema. Authorize must check the authenticated principal on every round and
// grant the caller's stable scoped StartID. An MCP request ID, untrusted header
// or diagnostic meta field alone is not authorization. If the SDK transport
// has no native session ID, the Host must also grant a trusted, stable dialog
// identity. Distinct principals and dialogs must not share it. A native session
// ID is bound independently whenever the transport exposes one. Rotating the
// secret resume key invalidates existing tokens. Replicas serving the same
// dialog must share the key, the exact Program and the durable start store.
//
// These tools require the pinned SDK's 2026-07-28 per-request protocol and form
// capability. Authorization and negotiation are checked before a fresh run
// starts. Each call waits for committed output or a confirmed external wait in
// its tree. One flat form with string and boolean fields is published per round.
// The prompt must be a string, and unsupported schemas, including nested objects and references,
// fail explicitly. Numeric forms are refused: the pinned SDK decodes form
// numbers into float64, which cannot preserve arbitrary JSON integers or
// decimals. Projection never removes an unsupported constraint.
//
// The caller receives an encrypted authenticated token holding a digest of the
// owner process, local wait key and expiry. Its size is independent of tree depth;
// committed run state resolves the process address, including reaped children
// recovered from history. It is bound to the authorized start, session, exact
// Program, tool and request arguments. Answers go through Handle.Answer; the
// Program alone owns decision semantics. Decline/cancel do not synthesize
// an answer or cancel the tree. Mismatched, expired and malformed tokens fail
// before mutation. Exact answer replay reuses the committed SignalAdmitted record;
// conflicting answers fail. After a caller disconnects, a fresh request with
// the same authorized StartID can inspect or rejoin the original run.
//
// Registration requires a readable authoritative head. A node without the
// writer can render a confirmed pending or terminal state and reconcile a
// previously accepted answer read-only. A new answer requires routing to the
// owning writer; this adapter never starts another run or takes the writer
// epoch when handling a continuation. The Host owns release, retention,
// recovery and shutdown. Successful terminal output uses the host's pure
// projection and the ordinary MCP tool result encoder. Logical termination
// is an MCP tool error; transport, authorization, admission and runtime errors
// are protocol errors and do not reveal private diagnostics.
package managed
