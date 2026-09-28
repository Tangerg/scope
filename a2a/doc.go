// Package a2a integrates the Agent-to-Agent protocol through a2a-go/v2.
//
// [OpenToolSet] resolves remote AgentCards and returns a [ToolSet] that owns
// the opened clients. [NewHTTPHandler] serves an [Agent] at the exact JSON-RPC
// path advertised by its card, plus the well-known AgentCard endpoint.
//
// Remote tools execute exclusively unless [Endpoint.ConcurrencyPolicy]
// declares which calls may safely overlap. HTTP JSON-RPC is the only transport
// exposed here. Client and server spans retain error classifications without
// raw messages; callers receive complete protocol errors.
package a2a
