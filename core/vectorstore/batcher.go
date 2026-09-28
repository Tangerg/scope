package vectorstore

import (
	"context"

	"github.com/Tangerg/scope/core/document"
)

type Batcher interface {
	// Batch partitions the supplied pointers without cloning or retaining them.
	// Every input pointer must occur exactly once in the output, global order is
	// preserved, and empty batches are invalid. Context cancellation remains
	// identifiable through errors.Is.
	Batch(ctx context.Context, documents []*document.Document) ([][]*document.Document, error)
}
