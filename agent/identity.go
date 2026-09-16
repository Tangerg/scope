package agent

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	maxIdentityBytes     = 256
	processIDPrefix      = "process:"
	effectIDPrefix       = "effect:"
	waitIDPrefix         = "wait:"
	engineSignalIDPrefix = "signal:engine:"
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

// Canonical hexadecimal identities share the identity representation, but their
// parsers own the prefix and width required by each domain.
func parseHexIdentity(value, prefix string, size int) (identity, error) {
	encoded, ok := strings.CutPrefix(value, prefix)
	if !ok || len(encoded) != size*2 || encoded != strings.ToLower(encoded) {
		return identity{}, ErrInvalidIdentity
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return identity{}, fmt.Errorf("%w: %w", ErrInvalidIdentity, err)
	}
	return identity{value: value}, nil
}

// Tags, identity parts, and decimal coordinates cannot contain NUL. Separating
// every component, including an explicit domain tag, makes their byte encoding
// unambiguous before hashing. Only this boundary assembles derived identities.
func deriveIdentity(prefix, tag string, parts ...string) identity {
	encoded := append([]string{tag}, parts...)
	digest := digestBytes([]byte(strings.Join(encoded, "\x00")))
	return identity{value: prefix + digest.hex()}
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

// Valid reports whether a value satisfies its invariant. For immutable values,
// constructors and decoders establish content invariants; Valid distinguishes
// their successful result from the invalid zero value without revalidating text.
// Caller-mutable values and enums must check their current content instead.
func (i identity) Valid() bool { return i.value != "" }

func (i identity) MarshalText() ([]byte, error) {
	if !i.Valid() {
		return nil, ErrInvalidIdentity
	}
	return []byte(i.value), nil
}

func (identity) JSONSchemaAlias() any { return "" }

// ProcessID is the stable identity of one Engine-owned Process.
type ProcessID struct{ identity }

// ParseProcessID validates an externally encoded Process identity.
func ParseProcessID(value string) (ProcessID, error) {
	id, err := parseIdentity("process ID", value)
	return ProcessID{id}, err
}

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

func (p ProcessID) effectID(step uint64, index int) EffectID {
	return EffectID{deriveIdentity(effectIDPrefix, "effect", p.String(), strconv.FormatUint(step, 10), strconv.Itoa(index))}
}

// SignalID is the stable identity used to deduplicate one Signal delivery.
type SignalID struct{ identity }

// ParseSignalID validates an externally supplied Signal delivery identity.
// Parsing does not accept or deliver the Signal. The signal:engine: namespace
// is reserved for Engine-generated delivery and cannot be used in SignalRequest.
func ParseSignalID(value string) (SignalID, error) {
	id, err := parseIdentity("signal ID", value)
	return SignalID{id}, err
}

func (s SignalID) engineOwned() bool { return strings.HasPrefix(s.String(), engineSignalIDPrefix) }

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

func (w WaitID) childWaitSignalID() SignalID {
	return SignalID{deriveIdentity(engineSignalIDPrefix, "child-wait-satisfied", w.String())}
}

// EffectID identifies one Effect at a stable Process, Step, and batch index.
type EffectID struct{ identity }

// ParseEffectID validates an externally encoded Effect identity.
func ParseEffectID(value string) (EffectID, error) {
	id, err := parseIdentity("effect ID", value)
	return EffectID{id}, err
}

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

func (e EffectID) waitID() WaitID {
	return WaitID{deriveIdentity(waitIDPrefix, "wait", e.String())}
}

func (e EffectID) settlementSignalID() SignalID {
	return SignalID{deriveIdentity(engineSignalIDPrefix, "signal", e.String())}
}

func (e EffectID) childProcessID() ProcessID {
	return ProcessID{deriveIdentity(processIDPrefix, "child", e.String())}
}

// WaitKey is an Execution-owned logical key used to associate a requested wait
// with the WaitID later minted by the Engine.
type WaitKey struct{ identity }

// ParseWaitKey validates an Execution-owned logical wait key.
func ParseWaitKey(value string) (WaitKey, error) {
	id, err := parseIdentity("wait key", value)
	return WaitKey{id}, err
}

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
