package vectorstore

import (
	"context"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type FilterDeleter interface {
	// DeleteWhere removes every document matching predicate. Implementations return
	// [ErrMissingFilter] for nil and reject invalid expressions.
	DeleteWhere(ctx context.Context, predicate filter.Predicate) error
}

type IDDeleter interface {
	// DeleteIDs removes the documents with the given ids. It rejects an id
	// that [ValidateDocumentID] rejects before any native call. Unknown ids
	// are ignored (idempotent), a repeated id acts once and an empty slice
	// is a no-op.
	DeleteIDs(ctx context.Context, ids []string) error
}
