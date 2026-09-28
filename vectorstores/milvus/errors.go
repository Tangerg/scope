package milvus

import "errors"

var (
	ErrMissingClient = errors.New("milvus: Client is required")

	ErrMissingCollectionName = errors.New("milvus: CollectionName is required")

	ErrMissingEmbeddingModel = errors.New("milvus: EmbeddingModel is required")

	ErrMissingDocumentBatcher = errors.New("milvus: DocumentBatcher is required")

	// ErrSchemaMismatch means the existing collection cannot preserve this
	// store's document, vector, or score contract.
	ErrSchemaMismatch = errors.New("milvus: collection schema or vector index does not match")

	ErrDocumentIDTooLong = errors.New("milvus: document ID exceeds the 36-byte limit")

	ErrDocumentContentTooLong = errors.New("milvus: document text exceeds the 65535-byte limit")
)
