// Package mcp provides Scope helpers around the Model Context Protocol
// (https://modelcontextprotocol.io/).
//
// Use the official Go SDK package (github.com/modelcontextprotocol/go-sdk/mcp)
// for protocol clients, servers, sessions, and transports. The Scope package
// keeps the small adapters needed around those SDK primitives:
// context metadata, reverse-capability helpers, tool.Tool wrapping, tool
// registration and prompt conversion.
//
// Client and server spans record error classifications without raw error
// messages. Callers still receive the complete protocol error details.
//
// # Naming
//
// The package shares its name with the official Go SDK
// (github.com/modelcontextprotocol/go-sdk/mcp). Consumers will normally
// import it as:
//
//	import (
//	    scopemcp "github.com/Tangerg/scope/mcp"
//	    sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
//	)
//
// Inside this package the SDK is imported under the alias sdkmcp.
package mcp
