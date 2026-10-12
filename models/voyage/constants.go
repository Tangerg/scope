package voyage

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Voyage"
)

const (
	EmbeddingRequestExtensionKey = "voyage/embedding_request"
	RerankRequestExtensionKey    = "voyage/rerank_request"

	// DefaultBaseURL is Voyage AI's production REST endpoint. Override
	// via [APIConfig.BaseURL] when proxying through an internal gateway.
	DefaultBaseURL = "https://api.voyageai.com/v1"

	Model4Large = "voyage-4-large"
	Model4      = "voyage-4"
	Model4Lite  = "voyage-4-lite"

	ModelRerank25     = "rerank-2.5"
	ModelRerank25Lite = "rerank-2.5-lite"
)
