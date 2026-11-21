// Command mcpagent obtains its system prompt and search tool from an MCP server.
// The stub model derives its brief from actual tool output and reports refusal
// explicitly. Request metadata is bound when the remote tools are discovered,
// because a durable call runs long after the caller's context is gone.
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
// freezes the canonical tool contract, and Compile digests it without
// caller-created configuration digests. The Program configures no Step-side
// callback, so it carries no Revision: the session, the authorization guard and
// the model run inside Effects, so reconnecting or revoking the grant keeps the
// exact binding and recovery never repeats an uncertain call. Ordinary tool calls remain in the
// interaction process.
//
// Cancellation notifies the peer without closing its session. A lost outcome
// remains Unknown through reconnection and cancellation; the canceled Exit
// retains it in the log instead of inventing a result. Runtime.Close stops local
// work while retaining that fact. A fresh execution can make a new call without
// advancing the canceled log. Connection health supplies no receipt or replay
// authority.
// The reconciliation example demonstrates external receipt investigation.
package main
