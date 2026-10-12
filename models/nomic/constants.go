package nomic

const (
	Provider = "Nomic"

	EmbeddingRequestExtensionKey = "nomic/embedding_request"

	DefaultBaseURL = "https://api-atlas.nomic.ai/v1"
)

// See https://docs.nomic.ai/reference/endpoints/nomic-embed-text for
// the current catalog.
const (
	ModelEmbedTextV15 = "nomic-embed-text-v1.5"
	ModelEmbedTextV1  = "nomic-embed-text-v1"
)

// Task types condition the encoder for asymmetric retrieval — pick
// search_query for the query side, search_document for the corpus side.
const (
	TaskTypeSearchQuery    = "search_query"
	TaskTypeSearchDocument = "search_document"
	TaskTypeClassification = "classification"
	TaskTypeClustering     = "clustering"
)
