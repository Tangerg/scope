package agent

import (
	"errors"
	"fmt"
	"time"
)

const (
	effectAttemptIDPrefix    = "attempt:"
	effectAttemptRandomBytes = 16
)

var ErrInvalidEffectAttemptID = errors.New("agent: invalid Effect attempt identity")

// EffectAttemptID identifies one actual Effect invocation, including replay and
// invocations after restore. It is observation metadata, not recovery state.
type EffectAttemptID struct{ identity }

// No Agent boundary accepts a caller-built attempt, so only UnmarshalText
// parses one.
func parseEffectAttemptID(value string) (EffectAttemptID, error) {
	id, err := parseHexIdentity(value, effectAttemptIDPrefix, effectAttemptRandomBytes)
	if err != nil {
		return EffectAttemptID{}, fmt.Errorf("%w: %w", ErrInvalidEffectAttemptID, err)
	}
	return EffectAttemptID{id}, nil
}

func newEffectAttemptID() EffectAttemptID {
	return EffectAttemptID{randomHexIdentity(effectAttemptIDPrefix, effectAttemptRandomBytes)}
}

func (e EffectAttemptID) MarshalText() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidEffectAttemptID
	}
	return []byte(e.value), nil
}

func (e *EffectAttemptID) UnmarshalText(text []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidEffectAttemptID)
	}
	value, err := parseEffectAttemptID(string(text))
	if err != nil {
		return err
	}
	*e = value
	return nil
}

type effectAttempt struct {
	id        EffectAttemptID
	startedAt time.Time
}
