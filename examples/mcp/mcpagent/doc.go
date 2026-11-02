// Command mcpagent obtains its system prompt and search tool from an MCP server.
// The stub model derives its brief from actual tool output and reports refusal
// explicitly. Request metadata travels through mcp.WithRequestMeta.
//
// Run from the repository root:
//
//	go run ./examples/mcp/mcpagent
//
// On Unix, the process-boundary tests build mcp/expert as a separate SDK
// executable, with no Scope imports or inherited environment. It makes no
// network or model calls. tools/process owns process lifetime, the SDK owns its
// session, Core Guard owns authorization, and Agent owns execution uncertainty.
//
//	cd examples
//	go test -race -count=1 -run '^TestMCPProcessBoundary$' ./mcp/mcpagent
//
// Host selection uses the original MCP source and remote identity. ToolSet
// freezes the canonical tool contract. Its revision identifies the expert
// artifact and opaque authorization callback; the interaction revision identifies
// the model query. Compile hashes those declarations without caller-created
// configuration digests. Session identity is excluded so reconnection can bind
// the same trusted artifact, while changing its grant refuses recovery before
// writer activation. Ordinary tool calls remain in the interaction process.
//
// Cancellation notifies the peer without closing its session. A lost outcome
// remains Unknown through reconnection and cancellation; the tree cannot emit
// Exited until the outcome is resolved. Runtime.Close stops local work while
// retaining that fact. A fresh execution can make a new call without advancing
// the unresolved log. Connection health supplies no receipt or replay authority.
// The reconciliation example demonstrates external receipt investigation.
package main
