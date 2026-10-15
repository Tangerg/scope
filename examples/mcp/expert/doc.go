// Command expert serves an offline research corpus over MCP stdio using only
// the native MCP SDK. It imports no Scope packages. The mcpagent tests build
// this separate executable and pin its content digest before connecting.
// All protocol traffic uses stdout; synchronization diagnostics use stderr.
//
// The search query "wait" blocks until protocol cancellation. "disconnect"
// exits after accepting a search and before returning its result. These are
// deterministic fault cuts for the reference integration, not a live service
// or a model-quality demonstration. The expert://status resource exposes its
// own process ID and physical call/cancellation counts for verification.
//
// Run the complete consumer checks from the examples module:
//
//	go test -count=1 -run '^TestMCPProcessBoundary$' ./mcp/mcpagent
//	GOWORK=off go test -count=1 -run '^TestMCPProcessBoundary$' ./mcp/mcpagent
package main
