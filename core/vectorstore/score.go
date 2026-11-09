package vectorstore

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
)

const (
	minimumCosineSimilarity = -1
	maximumCosineSimilarity = 1
	// cosineRoundingTolerance is how far a measured cosine may fall outside
	// [-1, 1] and still be read as the nearest endpoint. Engines compute it in
	// float32 over thousands of dimensions, so an exact duplicate can measure
	// slightly above one; a value farther out cannot come from a cosine and
	// still invalidates the result.
	cosineRoundingTolerance = 1e-3
)

// Score is a provider-neutral, query-relative relevance value in [0, 1]. Scores
// preserve ordering but are not comparable across providers or search modes.
// Its JSON value must be a number.
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
	return jsonv2.Marshal(wireScore(s))
}

func (s *Score) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: score receiver is nil", ErrInvalidScore)
	}
	var decoded *wireScore
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode score: %w", ErrInvalidScore, err)
	}
	if decoded == nil {
		return fmt.Errorf("%w: score must not be null", ErrInvalidScore)
	}
	candidate := Score(*decoded)
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
// Rounding within cosineRoundingTolerance of an endpoint maps to that
// endpoint; anything else outside the range becomes NaN.
func ScoreFromCosineSimilarity(similarity float64) Score {
	if math.IsNaN(similarity) ||
		similarity < minimumCosineSimilarity-cosineRoundingTolerance ||
		similarity > maximumCosineSimilarity+cosineRoundingTolerance {
		return Score(math.NaN())
	}
	similarity = min(maximumCosineSimilarity, max(minimumCosineSimilarity, similarity))
	return ScoreFromValue(
		(similarity - minimumCosineSimilarity) /
			(maximumCosineSimilarity - minimumCosineSimilarity),
	)
}

// ScoreFromCosineDistance maps 1-cosine-similarity from [0, 2] to [0, 1],
// with the same endpoint rounding as ScoreFromCosineSimilarity.
func ScoreFromCosineDistance(distance float64) Score {
	return ScoreFromCosineSimilarity(maximumCosineSimilarity - distance)
}

// ScoreFromDistance maps a non-negative, unbounded distance to (0, 1], where
// zero is an exact match. An invalid native distance cannot establish a match.
func ScoreFromDistance(distance float64) Score {
	if distance < 0 || math.IsNaN(distance) || math.IsInf(distance, 0) {
		return Score(math.NaN())
	}
	return Score(1 / (1 + distance))
}

// ScoreFromInnerProduct maps an unbounded dot product monotonically into [0, 1].
// Finite precision saturates the logistic function at both endpoints.
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

type wireScore Score

func (Score) JSONSchemaModel() any { return wireScore(0) }
