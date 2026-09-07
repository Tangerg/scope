package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxFailureCodeBytes    = 128
	maxFailureMessageBytes = 4096
)

var ErrInvalidFailure = errors.New("agent: invalid failure")

// FailureKind is the stable framework-level classification of a failed
// Process. It deliberately does not imply retryability or business semantics.
type FailureKind string

const (
	// FailureKindInvalid is the invalid zero value.
	FailureKindInvalid FailureKind = ""
	// FailureKindExecution identifies an ordinary Strategy execution failure.
	FailureKindExecution FailureKind = "execution"
	// FailureKindContract identifies a violated Framework or Strategy contract.
	FailureKindContract FailureKind = "contract"
	// FailureKindExternal identifies failed external infrastructure.
	FailureKindExternal FailureKind = "external"
	// FailureKindPanic identifies a recovered panic at an execution boundary.
	FailureKindPanic FailureKind = "panic"
)

func (f FailureKind) Valid() bool {
	switch f {
	case FailureKindExecution, FailureKindContract, FailureKindExternal, FailureKindPanic:
		return true
	default:
		return false
	}
}

func (f FailureKind) String() string {
	if !f.Valid() {
		return invalidEnumName
	}
	return string(f)
}

// Failure is an immutable, snapshot-safe classification and explanation. Code
// is stable for machine decisions; Message is diagnostic and must not contain
// secrets or unbounded external payloads.
type Failure struct {
	kind    FailureKind
	code    string
	message string
}

// NewFailure requires a kind and code alongside the message so callers
// classify failures programmatically. Matching on message text is what makes
// error handling break on wording changes, and it does not survive the
// snapshot round trip. Message must be trimmed, valid UTF-8 and contain at
// most 4096 bytes.
func NewFailure(kind FailureKind, code, message string) (Failure, error) {
	if !kind.Valid() {
		return Failure{}, fmt.Errorf("%w: kind is required", ErrInvalidFailure)
	}
	if !validQualifiedName(code) || len(code) > maxFailureCodeBytes {
		return Failure{}, fmt.Errorf("%w: code must be a lowercase qualified name containing at most %d bytes", ErrInvalidFailure, maxFailureCodeBytes)
	}
	if message == "" || strings.TrimSpace(message) != message || !utf8.ValidString(message) || len(message) > maxFailureMessageBytes {
		return Failure{}, fmt.Errorf("%w: message must be non-empty, trimmed UTF-8 within %d bytes", ErrInvalidFailure, maxFailureMessageBytes)
	}
	return Failure{kind: kind, code: code, message: message}, nil
}

// Kind returns the framework-level failure classification.
func (f Failure) Kind() FailureKind { return f.kind }

// Code returns the stable machine-readable reason.
func (f Failure) Code() string { return f.code }

// Message returns the bounded diagnostic explanation.
func (f Failure) Message() string { return f.message }

func (f Failure) Valid() bool {
	return f.kind.Valid() &&
		validQualifiedName(f.code) && f.message != ""
}

func (f Failure) MarshalJSON() ([]byte, error) {
	if !f.Valid() {
		return nil, ErrInvalidFailure
	}
	return json.Marshal(failureWire{Kind: f.kind, Code: f.code, Message: f.message})
}

func (f *Failure) UnmarshalJSON(data []byte) error {
	if f == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidFailure)
	}
	wire, err := wireJSON.decode[failureWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidFailure, err)
	}
	value, err := NewFailure(wire.Kind, wire.Code, wire.Message)
	if err != nil {
		return err
	}
	*f = value
	return nil
}

type failureWire struct {
	Kind    FailureKind `json:"kind"`
	Code    string      `json:"code"`
	Message string      `json:"message"`
}
