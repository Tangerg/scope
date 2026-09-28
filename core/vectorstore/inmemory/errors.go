package inmemory

import "errors"

const Provider = "InMemory"

var ErrMissingEmbeddingModel = errors.New("inmemory: embedding model is required")
