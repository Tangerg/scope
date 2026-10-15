// Package a2a integrates the Agent-to-Agent protocol through a2a-go/v2.
//
// [OpenToolSet] resolves remote AgentCards and returns a [ToolSet] that owns
// the opened clients. [NewHTTPHandler] serves an [Agent] at the exact JSON-RPC
// path advertised by its card, plus the well-known AgentCard endpoint. The Host
// supplies and owns the task store, so task retention and eviction are its
// responsibility rather than a hidden default.
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
// Remote tools make one SendMessage exchange. Tasks requiring further input or
// authentication remain ordinary RemoteAgentError values; the host must use
// the SDK when it needs to continue those tasks.
//
// Native Data parts are rendered from SDK-decoded values, not retained wire
// JSON. The SDK decodes generic numbers as float64, so Scope cannot promise
// exact numbers in native Data. A standalone null Data part is rejected by the
// SDK rather than treated as empty text. Recheck generic decoding when changing
// a2a-go; text parts retain their text.
// Independently encoded JSON-RPC and HTTP+JSON peers check result semantics;
// raw JSON-RPC callers exercise stored-task continuation and cancellation.
package a2a
