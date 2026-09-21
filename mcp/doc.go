// Package mcp provides Scope helpers around the Model Context Protocol
// (https://modelcontextprotocol.io/).
//
// Use the Scope-maintained Go SDK fork (github.com/Tangerg/go-sdk/mcp)
// for protocol clients, servers, sessions, and transports. The Scope package
// keeps the small adapters needed around those SDK primitives:
// context metadata, reverse-capability helpers, tool.Tool wrapping, tool
// registration and prompt conversion. The SDK decoder preserves exact JSON
// numbers and distinguishes explicit structured null from an omitted value;
// consumers must use the same SDK module path for protocol types.
//
// Client and server spans record error classifications without raw error
// messages. Callers still receive the complete protocol error details.
// Remote IsError results use core/tool.Failure to preserve every content part
// and structured detail. Register projects that same failure value back into
// the MCP result without flattening it to an error string. MCP does not encode
// refusal separately: remote IsError maps to FailureKindFailed, which does not
// imply that execution began. Unknown local outcomes and authorization errors
// become generic protocol errors, never model-visible internal diagnostics or
// definite Tool results. Input validation remains public Tool error feedback.
// Results requiring further input return ErrIncompleteResult without exposing
// unfinished content. This adapter does not implement multi-round-trip input
// fulfillment; hosts that need it must complete the exchange through the SDK.
//
// # Naming
//
// The package shares its name with the SDK
// (github.com/Tangerg/go-sdk/mcp). Consumers will normally
// import it as:
//
//	import (
//	    scopemcp "github.com/Tangerg/scope/mcp"
//	    sdkmcp "github.com/Tangerg/go-sdk/mcp"
//	)
//
// Inside this package the SDK is imported under the alias sdkmcp.
package mcp
