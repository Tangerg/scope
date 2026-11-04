package parser

import (
	"context"
	"errors"
	"io"
)

// Untrusted object, graphics-state and form nesting must fail before exhausting
// the process stack or retaining an unbounded sequence of saved states.
const maxNestingDepth = 1000

var ErrBudget = errors.New("pdf: parser resource budget exhausted")

// Limits applies to one complete parse, including all pages and repeated object
// resolution. Expanded bytes count every decoded filter stage and text output.
type Limits struct {
	MaxWork         int64
	MaxDecodedBytes int64
	MaxObjects      int64
}

type budget struct {
	ctx              context.Context
	limits           Limits
	work             int64
	decoded          int64
	remainingObjects int64
	depth            int
	err              error
}

func newBudget(ctx context.Context, limits Limits) *budget {
	return &budget{ctx: ctx, limits: limits, work: limits.MaxWork, decoded: limits.MaxDecodedBytes, remainingObjects: limits.MaxObjects}
}

func (b *budget) check() {
	if b.err == nil {
		b.err = b.ctx.Err()
	}
	if b.err != nil {
		panic(b.err)
	}
}

func (b *budget) consume(remaining *int64, amount int64) {
	b.check()
	if amount < 0 || amount > *remaining {
		b.err = ErrBudget
		panic(b.err)
	}
	*remaining -= amount
}

func (b *budget) step(amount int64)    { b.consume(&b.work, amount) }
func (b *budget) expand(amount int64)  { b.consume(&b.decoded, amount) }
func (b *budget) objects(amount int64) { b.consume(&b.remainingObjects, amount) }

func (b *budget) enter() {
	b.step(1)
	b.checkDepth(b.depth + 1)
	b.depth++
}

func (b *budget) checkDepth(depth int) {
	if depth > maxNestingDepth {
		b.err = ErrBudget
		panic(b.err)
	}
}

func (b *budget) leave() { b.depth-- }

type budgetReaderAt struct {
	source io.ReaderAt
	budget *budget
}

func (b *budgetReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	b.budget.step(int64(len(data)) + 1)
	n, err := b.source.ReadAt(data, offset)
	b.budget.check()
	return n, err
}

type budgetReader struct {
	source io.Reader
	budget *budget
}

func (b *budgetReader) Read(data []byte) (int, error) {
	b.budget.step(1)
	if int64(len(data)) > b.budget.decoded {
		data = data[:b.budget.decoded+1]
	}
	n, err := b.source.Read(data)
	b.budget.expand(int64(n))
	return n, err
}
