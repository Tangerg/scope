package pdf

import (
	"errors"

	"github.com/Tangerg/scope/etl/pdf/internal/parser"
)

const (
	DefaultMaxParseWork    = 64 << 20
	DefaultMaxDecodedBytes = 64 << 20
	DefaultMaxObjects      = 1_000_000
)

var ErrParseBudget = parser.ErrBudget

// ParseBudget bounds a complete Read, independently of compressed source size
// and declared page count. Work includes lexer bytes, source reads and object
// traversal. Decoded bytes count each stream decoding stage, conservative
// reservations for predictor and font buffers, and final extracted text;
// objects include xref entries and parsed object values. Zero fields use their
// corresponding defaults.
// Exhaustion discards all documents.
type ParseBudget struct {
	MaxWork         int64
	MaxDecodedBytes int64
	MaxObjects      int64
}

func (p ParseBudget) limits() (parser.Limits, error) {
	if p.MaxWork < 0 || p.MaxDecodedBytes < 0 || p.MaxObjects < 0 {
		return parser.Limits{}, errors.New("pdf reader: parser resource limits must not be negative")
	}
	if p.MaxWork == 0 {
		p.MaxWork = DefaultMaxParseWork
	}
	if p.MaxDecodedBytes == 0 {
		p.MaxDecodedBytes = DefaultMaxDecodedBytes
	}
	if p.MaxObjects == 0 {
		p.MaxObjects = DefaultMaxObjects
	}
	return parser.Limits{MaxWork: p.MaxWork, MaxDecodedBytes: p.MaxDecodedBytes, MaxObjects: p.MaxObjects}, nil
}
