package weaviate

import "errors"

var (
	ErrMissingClient = errors.New("weaviate: Client is required")

	ErrMissingClassName = errors.New("weaviate: ClassName is required")

	ErrMissingEmbeddingModel = errors.New("weaviate: EmbeddingModel is required")

	ErrMissingDocumentBatcher = errors.New("weaviate: DocumentBatcher is required")

	ErrInvalidObjectID = errors.New("weaviate: invalid object ID")

	ErrIncompatibleClass = errors.New("weaviate: existing class is incompatible")
)
