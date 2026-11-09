package perplexity

import "github.com/Tangerg/scope/models/protocol/openai"

const (
	Provider = "perplexity"
	BaseURL  = "https://api.perplexity.ai/v1"
	// RequestExtensionKey stores Agent API controls without a Core equivalent.
	RequestExtensionKey = Provider + "/request"
	// ResponseExtensionKey preserves the complete native terminal response,
	// including search results, citations, server tool output, and cost.
	ResponseExtensionKey = Provider + openai.ResponsesResponseExtensionSuffix
	// StreamEventExtensionKey preserves each exact native SSE event.
	StreamEventExtensionKey = Provider + "/responses_stream_event"
)
