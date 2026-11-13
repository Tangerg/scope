package tokenizer

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
)

var ErrInvalidLimit = errors.New("tokenizer: invalid token limit")

// Limit bounds text by the tokens its counter assigns. The zero value is
// unbounded; NewLimit pairs a positive maximum with the counter that measures
// against it, so neither can be configured without the other.
type Limit struct {
	maximum int
	counter TextCounter
}

func NewLimit(maximum int, counter TextCounter) (Limit, error) {
	if maximum <= 0 || lo.IsNil(counter) {
		return Limit{}, fmt.Errorf("%w: a positive maximum and a counter are required", ErrInvalidLimit)
	}
	return Limit{maximum: maximum, counter: counter}, nil
}

func (l Limit) Bounded() bool { return l.counter != nil }

func (l Limit) Max() int { return l.maximum }

// Count measures text against the limit's counter; an unbounded limit counts
// nothing. A negative count is a counter defect rather than a free text.
func (l Limit) Count(ctx context.Context, text string) (int, error) {
	if !l.Bounded() {
		return 0, nil
	}
	tokens, err := l.counter.CountText(ctx, text)
	if err != nil {
		return 0, err
	}
	if tokens < 0 {
		return 0, fmt.Errorf("%w: counter returned %d tokens", ErrInvalidLimit, tokens)
	}
	return tokens, nil
}
