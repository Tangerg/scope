package agent

import (
	"errors"
	"fmt"
)

const (
	maxIdentityBytes = 256
	processIDPrefix  = "process:"
	effectIDPrefix   = "effect:"
	waitIDPrefix     = "wait:"
	signalIDPrefix   = "signal:"
)

var ErrInvalidIdentity = errors.New("agent: invalid identity")

type identity struct {
	value string
}

func parseIdentity(kind, value string) (identity, error) {
	if !validIdentity(value) {
		return identity{}, fmt.Errorf("%w: %s must contain 1 to %d URI-safe ASCII characters", ErrInvalidIdentity, kind, maxIdentityBytes)
	}
	return identity{value: value}, nil
}

func validIdentity(value string) bool {
	if len(value) == 0 || len(value) > maxIdentityBytes {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_' || character == '.' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func (i identity) String() string { return i.value }

func (i identity) Valid() bool { return validIdentity(i.value) }

func (i identity) MarshalText() ([]byte, error) {
	if !i.Valid() {
		return nil, ErrInvalidIdentity
	}
	return []byte(i.value), nil
}

func (identity) JSONSchemaAlias() any { return "" }

func (i *identity) UnmarshalText(text []byte) error {
	if i == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidIdentity)
	}
	value, err := parseIdentity("value", string(text))
	if err != nil {
		return err
	}
	*i = value
	return nil
}

// ProcessID is the stable identity of one Engine-owned Process.
type ProcessID struct{ identity }

// ParseProcessID validates an externally encoded Process identity.
func ParseProcessID(value string) (ProcessID, error) {
	id, err := parseIdentity("process ID", value)
	return ProcessID{id}, err
}

func (p ProcessID) String() string { return p.identity.String() }

func (p ProcessID) Valid() bool { return p.identity.Valid() }

func (p ProcessID) MarshalText() ([]byte, error) { return p.identity.MarshalText() }

func (p *ProcessID) UnmarshalText(text []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil ProcessID receiver", ErrInvalidIdentity)
	}
	value, err := ParseProcessID(string(text))
	if err != nil {
		return err
	}
	*p = value
	return nil
}

// SignalID is the stable identity used to deduplicate one Signal delivery.
type SignalID struct{ identity }

// ParseSignalID validates an externally supplied Signal delivery identity.
// Parsing does not accept or deliver the Signal.
func ParseSignalID(value string) (SignalID, error) {
	id, err := parseIdentity("signal ID", value)
	return SignalID{id}, err
}

func (s SignalID) String() string { return s.identity.String() }

func (s SignalID) Valid() bool { return s.identity.Valid() }

func (s SignalID) MarshalText() ([]byte, error) { return s.identity.MarshalText() }

func (s *SignalID) UnmarshalText(text []byte) error {
	if s == nil {
		return fmt.Errorf("%w: nil SignalID receiver", ErrInvalidIdentity)
	}
	value, err := ParseSignalID(string(text))
	if err != nil {
		return err
	}
	*s = value
	return nil
}

// WaitID identifies one Engine-created wait target owned by a Process. Parsing
// a WaitID does not create a wait; the Engine rejects identities it did not mint.
type WaitID struct{ identity }

// ParseWaitID validates the wire representation of a Wait identity.
func ParseWaitID(value string) (WaitID, error) {
	id, err := parseIdentity("wait ID", value)
	return WaitID{id}, err
}

func (w WaitID) String() string { return w.identity.String() }

func (w WaitID) Valid() bool { return w.identity.Valid() }

func (w WaitID) MarshalText() ([]byte, error) { return w.identity.MarshalText() }

func (w *WaitID) UnmarshalText(text []byte) error {
	if w == nil {
		return fmt.Errorf("%w: nil WaitID receiver", ErrInvalidIdentity)
	}
	value, err := ParseWaitID(string(text))
	if err != nil {
		return err
	}
	*w = value
	return nil
}

// EffectID identifies one Effect at a stable Process, Step, and batch index.
type EffectID struct{ identity }

// ParseEffectID validates an externally encoded Effect identity.
func ParseEffectID(value string) (EffectID, error) {
	id, err := parseIdentity("effect ID", value)
	return EffectID{id}, err
}

func (e EffectID) String() string { return e.identity.String() }

func (e EffectID) Valid() bool { return e.identity.Valid() }

func (e EffectID) MarshalText() ([]byte, error) { return e.identity.MarshalText() }

func (e *EffectID) UnmarshalText(text []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil EffectID receiver", ErrInvalidIdentity)
	}
	value, err := ParseEffectID(string(text))
	if err != nil {
		return err
	}
	*e = value
	return nil
}

// WaitKey is an Execution-owned logical key used to associate a requested wait
// with the WaitID later minted by the Engine.
type WaitKey struct{ identity }

// ParseWaitKey validates an Execution-owned logical wait key.
func ParseWaitKey(value string) (WaitKey, error) {
	id, err := parseIdentity("wait key", value)
	return WaitKey{id}, err
}

func (w WaitKey) String() string { return w.identity.String() }

func (w WaitKey) Valid() bool { return w.identity.Valid() }

func (w WaitKey) MarshalText() ([]byte, error) { return w.identity.MarshalText() }

func (w *WaitKey) UnmarshalText(text []byte) error {
	if w == nil {
		return fmt.Errorf("%w: nil WaitKey receiver", ErrInvalidIdentity)
	}
	value, err := ParseWaitKey(string(text))
	if err != nil {
		return err
	}
	*w = value
	return nil
}

// ChildKey is an Execution-owned stable identity for one logical child start.
// The Engine combines it with the parent Process identity and prepared Effect
// identity to make retries and restoration idempotent.
type ChildKey struct{ identity }

// ParseChildKey validates an Execution-owned logical child identity.
func ParseChildKey(value string) (ChildKey, error) {
	id, err := parseIdentity("child key", value)
	return ChildKey{id}, err
}

func (c ChildKey) String() string { return c.identity.String() }

func (c ChildKey) Valid() bool { return c.identity.Valid() }

func (c ChildKey) MarshalText() ([]byte, error) { return c.identity.MarshalText() }

func (c *ChildKey) UnmarshalText(text []byte) error {
	if c == nil {
		return fmt.Errorf("%w: nil ChildKey receiver", ErrInvalidIdentity)
	}
	value, err := ParseChildKey(string(text))
	if err != nil {
		return err
	}
	*c = value
	return nil
}
