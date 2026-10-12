package jina

const (
	Provider = "Jina"

	EmbeddingRequestExtensionKey = "jina/embedding_request"

	DefaultBaseURL = "https://api.jina.ai/v1"
)

// See https://jina.ai/embeddings/ for the current catalog.
const (
	ModelEmbeddingsV5TextNano  = "jina-embeddings-v5-text-nano"
	ModelEmbeddingsV5TextSmall = "jina-embeddings-v5-text-small"
	ModelEmbeddingsV5OmniNano  = "jina-embeddings-v5-omni-nano"
	ModelEmbeddingsV5OmniSmall = "jina-embeddings-v5-omni-small"
	ModelEmbeddingsV4          = "jina-embeddings-v4"
	ModelEmbeddingsV3          = "jina-embeddings-v3"
	ModelEmbeddingsV2BaseEN    = "jina-embeddings-v2-base-en"
	ModelClipV2                = "jina-clip-v2"
	ModelColbertV2             = "jina-colbert-v2"

	ModelRerankerV35 = "jina-reranker-v3.5"
	ModelRerankerV3  = "jina-reranker-v3"
	ModelRerankerM0  = "jina-reranker-m0"
)
