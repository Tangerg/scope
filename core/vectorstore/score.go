package vectorstore

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
)

const (
	minimumCosineSimilarity = -1
	maximumCosineSimilarity = 1
)

// Score is a provider-neutral, query-relative relevance value in [0, 1]. Scores
// preserve ordering but are not comparable across providers or search modes.
type Score float64

func (s Score) Float64() float64 { return float64(s) }

func (s Score) Validate() error {
	value := float64(s)
	if math.IsNaN(value) || math.IsInf(value, 0) || value < MinRelevanceScore || value > MaxRelevanceScore {
		return fmt.Errorf("%w: must be finite and in [%.1f, %.1f], got %v",
			ErrInvalidScore, MinRelevanceScore, MaxRelevanceScore, value)
	}
	return nil
}

func (s Score) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	type wireScore Score
	return json.Marshal(wireScore(s))
}

func (s *Score) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: score receiver is nil", ErrInvalidScore)
	}
	type wireScore Score
	var decoded wireScore
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode score: %w", ErrInvalidScore, err)
	}
	candidate := Score(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// ScoreFromValue clamps a finite provider score to the common range.
// Non-finite input becomes NaN so result validation reports the contract breach.
func ScoreFromValue(value float64) Score {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Score(math.NaN())
	}
	return Score(min(MaxRelevanceScore, max(MinRelevanceScore, value)))
}

// ScoreFromCosineSimilarity maps cosine similarity from [-1, 1] to [0, 1].
func ScoreFromCosineSimilarity(similarity float64) Score {
	return ScoreFromValue(
		(similarity - minimumCosineSimilarity) /
			(maximumCosineSimilarity - minimumCosineSimilarity),
	)
}

// ScoreFromCosineDistance maps 1-cosine-similarity from [0, 2] to [0, 1].
func ScoreFromCosineDistance(distance float64) Score {
	return ScoreFromCosineSimilarity(maximumCosineSimilarity - distance)
}

// ScoreFromDistance maps a non-negative, unbounded distance to (0, 1], where
// zero is an exact match. Tiny negative values caused by floating-point error
// are treated as zero.
func ScoreFromDistance(distance float64) Score {
	if math.IsNaN(distance) || math.IsInf(distance, 0) {
		return Score(math.NaN())
	}
	return Score(1 / (1 + max(0, distance)))
}

// ScoreFromInnerProduct maps an unbounded dot product monotonically into
// (0, 1).
func ScoreFromInnerProduct(product float64) Score {
	if math.IsNaN(product) || math.IsInf(product, 0) {
		return Score(math.NaN())
	}
	if product >= 0 {
		exponential := math.Exp(-product)
		return Score(1 / (1 + exponential))
	}
	exponential := math.Exp(product)
	return Score(exponential / (1 + exponential))
}

// ScoreFromNegativeInnerProductDistance maps a provider distance defined as
// the negative dot product into the similarity range.
func ScoreFromNegativeInnerProductDistance(distance float64) Score {
	return ScoreFromInnerProduct(-distance)
}

// ScoreFromOneMinusInnerProductDistance maps a provider distance defined as
// 1-dot-product into the similarity range.
func ScoreFromOneMinusInnerProductDistance(distance float64) Score {
	return ScoreFromInnerProduct(1 - distance)
}
