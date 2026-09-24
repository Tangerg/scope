package eval

import (
	"fmt"
	"math"
)

// Score is a normalized quality score in the closed interval [0, 1], where a
// higher value is always better.
type Score float64

// NewScore rejects non-finite and out-of-range values at construction.
func NewScore(value float64) (Score, error) {
	score := Score(value)
	if err := score.Validate(); err != nil {
		return 0, err
	}
	return score, nil
}

func (s Score) Float64() float64 { return float64(s) }

func (s Score) Validate() error {
	value := s.Float64()
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("%w: must be between 0 and 1", ErrInvalidScore)
	}
	return nil
}

func (s Score) Verdict(threshold Score) (Verdict, error) {
	if err := s.Validate(); err != nil {
		return VerdictUnspecified, err
	}
	if err := threshold.Validate(); err != nil {
		return VerdictUnspecified, fmt.Errorf("eval: threshold: %w", err)
	}
	if s >= threshold {
		return VerdictPass, nil
	}
	return VerdictFail, nil
}
