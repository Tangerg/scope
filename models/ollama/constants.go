package ollama

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Ollama"
)

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	// EmbeddingRequestExtensionKey carries provider-only /api/embed fields.
	// Model, input texts, and dimensions are owned by Core and are rejected here.
	EmbeddingRequestExtensionKey = "ollama/embedding_request"

	// DefaultBaseURL is Ollama's default local listen address.
	DefaultBaseURL = "http://127.0.0.1:11434"

	// OpenAICompatPath is the suffix Ollama serves the OpenAI-compatible
	// API under. [NewChatCompletions] joins it with the configured host.
	OpenAICompatPath = "/v1"
)
