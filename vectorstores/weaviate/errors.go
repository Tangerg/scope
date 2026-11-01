package weaviate

import "errors"

var (
	ErrMissingClient = errors.New("weaviate: Client is required")

	ErrMissingCollection = errors.New("weaviate: Collection is required")

	ErrInvalidObjectID = errors.New("weaviate: invalid object ID")

	ErrIncompatibleCollection = errors.New("weaviate: existing collection is incompatible")
)
