package milvus

import "errors"

var (
	ErrMissingClient = errors.New("milvus: Client is required")

	ErrMissingCollectionName = errors.New("milvus: CollectionName is required")

	ErrMissingEmbeddingModel = errors.New("milvus: EmbeddingModel is required")

	ErrMissingDocumentBatcher = errors.New("milvus: DocumentBatcher is required")

	// ErrSchemaMismatch means the existing collection cannot preserve this
	// store's document, vector, or score contract.
	ErrSchemaMismatch = errors.New("milvus: collection schema or vector index does not have the current policy")

	ErrDocumentIDTooLong = errors.New("milvus: document ID exceeds the native VARCHAR capacity")

	ErrDocumentContentTooLong = errors.New("milvus: document text exceeds the native VARCHAR capacity")
)
