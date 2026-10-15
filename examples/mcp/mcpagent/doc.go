// Command mcpagent runs an interaction agent whose system prompt and search
// tool come from an MCP server, forwarding request metadata with
// mcp.WithRequestMeta. The stub model derives its brief from actual tool output
// and reports refusal explicitly. The runnable demonstration remains offline.
//
// Run from the repository root:
//
//	go run ./examples/mcp/mcpagent
//
// On Unix, the cross-process checks build mcp/expert as a separate native-SDK
// executable, with no Scope imports or inherited environment. It makes no
// network or model calls. The Host composes tools/process with the SDK IOTransport;
// the process backend owns start/wait/group cleanup, the SDK owns the session,
// Core Guard owns Tool authorization, and Agent owns execution and uncertainty.
// An explicit working directory and process group are not an OS sandbox.
//
// From the examples module:
//
//	go test -race -count=1 -run '^TestMCPProcessBoundary$' ./mcp/mcpagent
//	GOWORK=off go test -count=1 -run '^TestMCPProcessBoundary$' ./mcp/mcpagent
//
// The Host selects the original source/remote identity before exposing tools.
// Its frozen Tool binding includes the expert artifact digest, model-visible
// Core Contract, and authorization grant. Contract supplies canonical schema
// bytes rather than the SDK's map encoding. NewDeployment owns transitive child
// binding identity; the parent does not copy child references into its config.
// A replacement session requires rediscovery and an exact Deployment binding.
// Changing a grant refuses restoration before writer activation. The same
// trusted expert artifact must remain available; these checks are not a
// general remote-service version or compatibility mechanism.
//
// Canceling a call notifies the peer without closing its session. A lost result
// remains Unknown, including after reconnection, restoration, and cancellation.
// Connection health is not a receipt. Only a fresh execution may make a new
// call; reconnection cannot authorize replay or resolve an older Effect.
// Agent/reconciliation demonstrates explicit external-outcome investigation.
// These checks prove the Scope-side reference composition. A deployed external
// expert's authentication, business receipts, retention, and product workflow
// remain with its consuming Host.
package main
