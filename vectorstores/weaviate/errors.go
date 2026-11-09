package weaviate

import "errors"

var (
	ErrMissingClient = errors.New("weaviate: Client is required")

	ErrUninitializedClient = errors.New("weaviate: Client must be created with NewClient")

	ErrMissingCollection = errors.New("weaviate: Collection is required")

	ErrInvalidObjectID = errors.New("weaviate: invalid object ID")

	ErrIncompatibleCollection = errors.New("weaviate: existing collection is incompatible")
)
