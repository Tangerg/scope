// Package elicitation names the MCP form-elicitation vocabulary that the pinned
// SDK leaves as bare strings. The remote-tool client and the managed server
// speak the same protocol, so they share one declaration of it.
package elicitation

// firstProtocolVersion is the first MCP revision with per-request form
// elicitation, including input requests carried by a tool result.
const firstProtocolVersion = "2026-07-28"

// Supported reports whether a negotiated revision carries per-request form
// elicitation. MCP revisions are ISO dates, so later revisions sort after it.
func Supported(protocolVersion string) bool {
	return protocolVersion >= firstProtocolVersion
}

const FormMode = "form"

const ObjectSchemaType = "object"

// Only Accept carries content.
const (
	Accept  = "accept"
	Decline = "decline"
	Cancel  = "cancel"
)
