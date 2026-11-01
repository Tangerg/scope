package origin

import (
	"context"
	"io"
	"sync/atomic"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

// Stream admits one read of a sequential source, including a read that fails
// after advancing its offset. A caller cannot attest its suffix as a revision.
type Stream struct {
	source   io.Reader
	admitted atomic.Bool
}

func NewStream(source io.Reader) *Stream { return &Stream{source: source} }

func (s *Stream) Read(ctx context.Context, budget etl.SourceBudget, name string) ([]byte, *document.Provenance, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !s.admitted.CompareAndSwap(false, true) {
		return nil, nil, etl.ErrSourceConsumed
	}
	return Read(ctx, s.source, budget, name)
}
