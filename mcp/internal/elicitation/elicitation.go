// Package elicitation names the MCP form-elicitation vocabulary that the pinned
// SDK leaves as bare strings. The remote-tool client and the managed server
// speak the same protocol, so they share one declaration of it.
package elicitation

// ProtocolVersion is the first MCP revision with per-request form elicitation,
// including input requests carried by a tool result.
const ProtocolVersion = "2026-07-28"

const FormMode = "form"

const ObjectSchemaType = "object"

// Only Accept carries content.
const (
	Accept  = "accept"
	Decline = "decline"
	Cancel  = "cancel"
)
