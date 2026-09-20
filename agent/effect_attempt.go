package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	effectAttemptIDPrefix    = "attempt:"
	effectAttemptRandomBytes = 16
)

var ErrInvalidEffectAttemptID = errors.New("agent: invalid Effect attempt identity")

// EffectAttemptID identifies one actual Effect invocation. Each invocation gets a
// fresh random identity, including replay and invocations after restore in both
// durable and ephemeral trees. It is observation metadata, not Process recovery state.
type EffectAttemptID struct{ identity }

// ParseEffectAttemptID validates the canonical wire representation of an
// Effect attempt identity.
func ParseEffectAttemptID(value string) (EffectAttemptID, error) {
	id, err := parseHexIdentity(value, effectAttemptIDPrefix, effectAttemptRandomBytes)
	if err != nil {
		return EffectAttemptID{}, fmt.Errorf("%w: %w", ErrInvalidEffectAttemptID, err)
	}
	return EffectAttemptID{id}, nil
}

func newEffectAttemptID() EffectAttemptID {
	var random [effectAttemptRandomBytes]byte
	rand.Read(random[:])
	return EffectAttemptID{identity{value: effectAttemptIDPrefix + hex.EncodeToString(random[:])}}
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
	value, err := ParseEffectAttemptID(string(text))
	if err != nil {
		return err
	}
	*e = value
	return nil
}

// effectAttempt binds duration and observation identity to the same invocation.
type effectAttempt struct {
	id        EffectAttemptID
	startedAt time.Time
}
