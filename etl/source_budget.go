package etl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/samber/lo"
)

// DefaultMaxSourceBytes is the bounded zero-value policy for whole-source
// readers.
const DefaultMaxSourceBytes int64 = 32 * 1024 * 1024

const maxSupportedSourceBytes = math.MaxInt64 - 1

var (
	// ErrInvalidSourceBudget identifies a non-positive or unrepresentable bound.
	ErrInvalidSourceBudget = errors.New("etl: invalid source budget")
	// ErrNilSource rejects an absent reader before attempting extraction.
	ErrNilSource = errors.New("etl: source must not be nil")
	// ErrSourceTooLarge reports that no partial payload is returned.
	ErrSourceTooLarge = errors.New("etl: source exceeds byte budget")
)

// SourceBudget is the shared memory-safety contract for whole-source readers.
// Its zero value uses [DefaultMaxSourceBytes]. A custom budget must be created
// with [NewSourceBudget], so readers never have an implicit unlimited mode.
type SourceBudget struct {
	maxBytes int64
}

// NewSourceBudget makes a custom whole-source memory bound explicit.
func NewSourceBudget(maxBytes int64) (SourceBudget, error) {
	if maxBytes <= 0 || maxBytes > maxSupportedSourceBytes {
		return SourceBudget{}, fmt.Errorf(
			"%w: max bytes must be in [1, %d]",
			ErrInvalidSourceBudget,
			maxSupportedSourceBytes,
		)
	}
	return SourceBudget{maxBytes: maxBytes}, nil
}

func (s SourceBudget) MaxBytes() int64 {
	if s.maxBytes == 0 {
		return DefaultMaxSourceBytes
	}
	return s.maxBytes
}

// ReadAll consumes at most MaxBytes plus one detection byte. It returns no
// partial payload when the source exceeds the budget and preserves source and
// context errors for errors.Is/errors.As. Cancellation stops subsequent reads;
// interruption of an in-flight Read remains the source's responsibility.
func (s SourceBudget) ReadAll(ctx context.Context, source io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if lo.IsNil(source) {
		return nil, ErrNilSource
	}
	maxBytes := s.MaxBytes()
	data, err := io.ReadAll(io.LimitReader(sourceReader{ctx: ctx, source: source}, maxBytes+1))
	if err = errors.Join(err, ctx.Err()); err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrSourceTooLarge, maxBytes)
	}
	return data, nil
}

type sourceReader struct {
	ctx    context.Context
	source io.Reader
}

func (s sourceReader) Read(buffer []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	return s.source.Read(buffer)
}
