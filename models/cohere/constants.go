package cohere

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "cohere"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	EmbeddingRequestExtensionKey = Provider + "/embedding_request"
	RerankRequestExtensionKey    = Provider + "/rerank_request"

	RerankRequestIDMetadataKey   = "cohere/request_id"
	RerankSearchUnitsMetadataKey = "cohere/search_units"
	BilledInputTokensMetadataKey = "cohere/billed_input_tokens"
)

// ModelRerankV35 names the rerank model this adapter is verified against, so a
// caller can pin it without copying a version string that moves.
const ModelRerankV35 = "rerank-v3.5"
