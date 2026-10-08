// Package mcp adapts Scope tools and prompts to the Model Context Protocol
// (https://modelcontextprotocol.io/).
//
// Protocol clients, servers, sessions, and transports come from the
// Scope-maintained SDK fork, github.com/Tangerg/go-sdk/mcp; consumers must use
// that module path for protocol types. Its decoder keeps exact numbers in
// structuredContent and distinguishes explicit null from absence.
//
// Remote IsError results become [tool.Failure] values of kind
// [tool.FailureKindFailed] with every content part and structured detail;
// because MCP does not encode refusal, that kind does not imply execution
// began. [Register] projects the same Failure back into an IsError result.
// Invalid arguments stay model-visible error results, while unknown local
// outcomes and authorization errors become generic protocol errors, never
// definite results or internal diagnostics. Results that require further
// input return [ErrIncompleteResult]; hosts that need multi-round-trip input
// must complete that exchange through the SDK. Successful structured results
// are validated against the output schema frozen at discovery.
//
// Spans record error classifications, never raw error messages.
//
// Tool results and prompts share one content codec. [ContentMetadataKey]
// carries MCP annotations, resource provenance, and presentation metadata
// that Core has no field for, and Core metadata, citations, and media names,
// IDs, and metadata that MCP has no field for travel through MCP _meta as JSON
// text so their numbers survive. A Core media reference is an opaque handle
// valid only with the provider that issued it, so serving one fails with
// [ErrMediaReference]; a received resource link is always a URI. When a
// server declares no media type, the client infers one from the URI extension
// with a fixed table independent of the host, falls back to
// application/octet-stream, and marks the envelope so that serving the content
// again omits the guess. Empty text without metadata is omitted; malformed or
// unsupported content is rejected. The SDK decodes outputSchema and native
// _meta numbers as float64, so precision beyond that is lost before Scope
// receives it.
package mcp
