package weaviate

import "errors"

var (
	ErrMissingClient = errors.New("weaviate: Client is required")

	ErrMissingClassName = errors.New("weaviate: ClassName is required")

	ErrInvalidObjectID = errors.New("weaviate: invalid object ID")

	ErrIncompatibleClass = errors.New("weaviate: existing class is incompatible")
)
