package openai

import (
	"fmt"
	"time"

	corechat "github.com/Tangerg/scope/core/chat"
)

// A single native response may repeat its identity on content or usage events;
// an omitted field adds no evidence. A mismatched tail cannot close another call.
type streamIdentity struct {
	id      string
	model   string
	created int64
}

func (s *streamIdentity) bind(id, model string, created int64) error {
	if id != "" && s.id != "" && id != s.id {
		return fmt.Errorf("%w: openai: streamed response ID changed", corechat.ErrInvalidResponse)
	}
	if model != "" && s.model != "" && model != s.model {
		return fmt.Errorf("%w: openai: streamed model changed", corechat.ErrInvalidResponse)
	}
	if created > 0 && s.created > 0 && created != s.created {
		return fmt.Errorf("%w: openai: streamed creation time changed", corechat.ErrInvalidResponse)
	}
	if id != "" {
		s.id = id
	}
	if model != "" {
		s.model = model
	}
	if created > 0 {
		s.created = created
	}
	return nil
}

func (s streamIdentity) metadata() *corechat.ResponseMetadata {
	m := &corechat.ResponseMetadata{ID: s.id, Model: s.model}
	if s.created > 0 {
		m.CreatedAt = time.Unix(s.created, 0).UTC()
	}
	return m
}
