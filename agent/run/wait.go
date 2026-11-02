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

func (w Wait) admits(payload Payload) error {
	limit := w.MaxAnswerBytes
	if limit == 0 {
		limit = MaxPayloadBytes
	}
	if !payload.Valid() || len(payload.data) > limit {
		return fmt.Errorf("%w: answer size", ErrSignalRejected)
	}
	if w.AnswerSchema.Valid() {
		if err := w.AnswerSchema.Validate(payload.data); err != nil {
			return fmt.Errorf("%w: %w", ErrSignalRejected, err)
		}
	}
	return nil
}
