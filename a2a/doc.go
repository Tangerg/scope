// Package a2a integrates the Agent-to-Agent protocol through a2a-go/v2.
//
// [OpenToolSet] resolves remote AgentCards and returns a [ToolSet] that owns
// the opened clients. [NewHTTPHandler] serves an [Agent] at the exact JSON-RPC
// path advertised by its card, plus the well-known AgentCard endpoint. The Host
// supplies and owns the task store, so task retention and eviction are its
// responsibility rather than a hidden default.
// Discovery has independent time and decoded-body limits configured by
// [Endpoint.CardTimeout] and [Endpoint.CardMaxBytes]; neither limits RPC work.
// A served handler bounds each RPC request body by
// [ServerConfig.MaxRequestBytes] before the SDK decodes it.
//
// [Endpoint.ConcurrencyPolicy] supplies Host scheduling declarations for remote
// tools; [ToolConcurrencyPolicy] describes their scope. The served handler is
// JSON-RPC; the client opens the JSON-RPC and HTTP+JSON transports a card
// advertises. Client and server spans retain error classifications without raw
// messages; callers receive complete protocol errors.
//
// The SDK owns task state, history, artifact accumulation, and cancellation.
// Serving a continuation emits updates to that existing task rather than a
// replacement Task. Agent.Run receives the current message; retained protocol
// history is not an implicit conversational context for the Agent.
// An input-required remote task becomes a tool.InputRequest bound to the
// original task, context, and tool binding. A validated text answer reuses
// the reply identity and continues the same task. Authentication requests
// require explicit Host handling.
// Durable Hosts set Endpoint.ContinuationIdentity from their stable principal
// and execution revision, and use that same revision in the interaction's ToolSet
// declaration. The adapter also binds discovery, remote interfaces and tool name.
// Reconstructing an equivalent ToolSet can then resume a saved InputRequest;
// changing its authority rejects the continuation before RPC dispatch. With an
// empty identity, continuations belong only to the opened ToolSet's lifetime.
//
// The text boundary rejects structured, binary and URI parts explicitly.
// Independently encoded JSON-RPC and HTTP+JSON peers check result semantics;
// raw JSON-RPC callers exercise stored-task continuation and cancellation.
// Raw peers must offer the interface's advertised protocol version using
// A2A-Version; an omitted version does not implicitly select this service.
package a2a
