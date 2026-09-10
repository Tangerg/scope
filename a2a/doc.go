// Package a2a integrates the Agent-to-Agent (A2A) protocol into the Scope
// agent framework, wrapping the official SDK
// github.com/a2aproject/a2a-go/v2 ([sdka2a]/[a2asrv]/[a2aclient]).
//
// It has two sides:
//
//   - CLIENT — [OpenToolSet] resolves remote AgentCards and returns a
//     [ToolSet] that owns both the tool view and opened protocol clients.
//
//   - SERVER — expose a capability AS an A2A endpoint. Implement the
//     narrow [Agent] interface (text in, streamed text out); [NewHTTPHandler]
//     adapts it to the SDK and mounts the JSON-RPC method endpoint plus the
//     well-known AgentCard.
//
// The server mounts the exact path advertised by the card's sole JSON-RPC
// interface. Remote tools execute exclusively unless the host supplies an
// [Endpoint.ConcurrencyPolicy] that declares which calls may safely overlap.
//
// The transport default is JSON-RPC over HTTP, matching the rest of the
// stack; the SDK's REST/gRPC bindings are not precluded but are not wired
// here.
//
// Client and server spans record error classifications without raw error
// messages. Callers still receive the complete protocol error details.
//
// Naming convention: the SDK's core types package is imported as `sdka2a`
// to avoid colliding with this package's own name; the server and client
// SDK packages keep their names `a2asrv` / `a2aclient`.
package a2a
