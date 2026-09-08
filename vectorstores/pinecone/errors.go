package pinecone

import "errors"

var (
	ErrMissingClient = errors.New("pinecone: Client is required")

	ErrMissingIndexHost = errors.New("pinecone: IndexHost is required")

	ErrMissingEmbeddingModel = errors.New("pinecone: EmbeddingModel is required")

	ErrMissingDocumentBatcher = errors.New("pinecone: DocumentBatcher is required")

	ErrMissingDistanceMetric = errors.New("pinecone: DistanceMetric is required")

	// ErrIncompatibleIndex reports an index that is not the one the store was
	// configured for: either nothing is served at IndexHost, or the index
	// there was created with a different distance metric.
	ErrIncompatibleIndex = errors.New("pinecone: index is incompatible")
)
