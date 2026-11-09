// Package history defines provider-neutral conversation history contracts and
// model middleware. Persistent backends live in independent historystores
// modules; the reference implementation lives in core/history/inmemory.
//
// Each Batch owns an immutable append identity and canonical message snapshot.
// Writer atomically accepts the complete batch, including its deduplication
// evidence, and returns a Receipt. A retry submits the same Batch: identical
// content returns the same receipt, while different content conflicts. Missing
// acknowledgment never authorizes a new append identity. Message retention in
// an open conversation must not discard receipt evidence. Clear permanently
// closes a ConversationID, so
// delayed appends cannot resurrect deleted history. A new conversation needs a
// new identity.
//
// Middleware binds one ConversationID to explicit Reader and Writer
// capabilities. Requests supply a current system prefix and one user-led open
// turn. History never holds system messages: Batch and DecodeMessages reject
// them with ErrSystemMessage, so a stored prompt cannot compete with the
// request's. Tool follow-ups resubmit that turn's user message, assistant tool
// calls, and tool results. The stored history supplies prior completed turns;
// a completed invocation appends its final assistant and open turn in one
// Batch.
// Callers supplying a complete transcript use the undecorated model.
//
// CommitError preserves the Batch and any confirmed Receipt after generation
// succeeds but persistence fails. A host can retry that Batch without repeating
// model generation. The completed response and usage remain available to the
// caller. Invalid input fails before store or provider I/O.
//
// WindowReader is a read projection that retains complete user-led turns,
// including their tool exchanges. Compose it with an explicit Writer; a read
// projection does not acquire write authority. The host owns conversation turn
// ordering: concurrent-safe store methods do not isolate the entire
// Read/model/Append sequence across calls or host instances.
package history
