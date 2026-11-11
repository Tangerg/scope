package run

import (
	"fmt"

	"github.com/Tangerg/scope/core/jsonschema"
)

type Wait struct {
	Key            WaitKey           `json:"key"`
	Question       Payload           `json:"question,omitzero"`
	AnswerSchema   jsonschema.Schema `json:"answer_schema,omitzero"`
	MaxAnswerBytes int               `json:"max_answer_bytes"`
}

func (w Wait) Valid() bool {
	return w.Key.Valid() && w.MaxAnswerBytes >= 0 && w.MaxAnswerBytes <= MaxPayloadBytes
}

// equal treats an absent answer schema as its own value: a wait that validates
// answers is never the same promise as one that admits any payload.
func (w Wait) equal(other Wait) bool {
	if w.Key != other.Key || w.MaxAnswerBytes != other.MaxAnswerBytes || !w.Question.Equal(other.Question) {
		return false
	}
	if !w.AnswerSchema.Valid() || !other.AnswerSchema.Valid() {
		return !w.AnswerSchema.Valid() && !other.AnswerSchema.Valid()
	}
	return w.AnswerSchema.Equal(other.AnswerSchema)
}

// Admits is the one admission rule for an answer to this Wait: the byte bound,
// where zero means MaxPayloadBytes, and then the answer schema. The Runtime
// applies it to every addressed Signal; Hosts may check an answer early with it.
func (w Wait) Admits(payload Payload) error {
	limit := w.MaxAnswerBytes
	if limit == 0 {
		limit = MaxPayloadBytes
	}
	if !payload.Valid() || payload.Size() > limit {
		return fmt.Errorf("%w: answer size", ErrSignalRejected)
	}
	if w.AnswerSchema.Valid() {
		if err := w.AnswerSchema.Validate(payload.data); err != nil {
			return fmt.Errorf("%w: %w", ErrSignalRejected, err)
		}
	}
	return nil
}
