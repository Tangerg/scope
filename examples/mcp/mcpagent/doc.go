// Command mcpagent runs an interaction agent whose system prompt and search
// tool come from an MCP server, forwarding request metadata with
// mcp.WithRequestMeta. In-memory transports and a stub model keep it offline.
//
// Run from the repository root:
//
//	go run ./examples/mcp/mcpagent
package main
