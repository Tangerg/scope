package eval

import (
	"fmt"
	"math"

	"github.com/Tangerg/scope/core/metadata"
)

// Score is a normalized quality score in the closed interval [0, 1], where a
// higher value is always better.
type Score float64

const (
	thresholdPolicy    = "threshold"
	thresholdParameter = "threshold"
)

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

// Decide applies the higher-is-better threshold without changing the identity
// of the calculation that produced the Score.
func (s Score) Decide(threshold Score) (Decision, error) {
	if err := s.Validate(); err != nil {
		return Decision{}, err
	}
	if err := threshold.Validate(); err != nil {
		return Decision{}, fmt.Errorf("eval: threshold: %w", err)
	}
	parameters := metadata.Map{}
	if err := parameters.Set(thresholdParameter, threshold); err != nil {
		return Decision{}, fmt.Errorf("eval: threshold identity: %w", err)
	}
	decision := Decision{Policy: thresholdPolicy, Parameters: parameters, Verdict: VerdictFail}
	if s >= threshold {
		decision.Verdict = VerdictPass
	}
	return decision, nil
}
