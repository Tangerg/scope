package perplexity

const (
	Provider = "Perplexity"
	BaseURL  = "https://api.perplexity.ai/v1"
	// RequestExtensionKey stores Agent API controls without a Core equivalent.
	RequestExtensionKey = "perplexity/request"
	// ResponseExtensionKey preserves the complete native terminal response,
	// including search results, citations, server tool output, and cost.
	ResponseExtensionKey = "perplexity/responses_response"
	// StreamEventExtensionKey preserves each exact native SSE event.
	StreamEventExtensionKey = "perplexity/responses_stream_event"
)
