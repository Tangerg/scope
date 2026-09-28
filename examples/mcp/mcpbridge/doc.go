// Command mcpbridge exposes a Scope tool through MCP using in-memory transports.
//
// Run from the repository root:
//
//	go run ./examples/mcp/mcpbridge
//
// A deployed server can use sdkmcp.StdioTransport or a Streamable HTTP handler.
package main
