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

// FailureKind stays independent of retryability so classifying a failure
// cannot silently select a recovery or business policy.
type FailureKind string

const (
	FailureKindInvalid   FailureKind = ""
	FailureKindExecution FailureKind = "execution"
	FailureKindContract  FailureKind = "contract"
	FailureKindExternal  FailureKind = "external"
	FailureKindPanic     FailureKind = "panic"
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

// Failure separates stable codes from diagnostic text so wording changes cannot
// alter control flow. Its bounded UTF-8 message must survive snapshot JSON and
// must exclude secrets.
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

func (f Failure) Kind() FailureKind { return f.kind }

func (f Failure) Code() string { return f.code }

func (f Failure) Message() string { return f.message }

func (f Failure) Valid() bool {
	return f.kind.Valid() &&
		validQualifiedName(f.code) && f.message != ""
}

// Kernel classifications are fixed by their owning boundary. An invalid kind or
// code is a programming error; substituting another Failure would hide its cause.
func newEngineFailure(kind FailureKind, code string, err error) Failure {
	message := "unknown error"
	if err != nil {
		// Go errors can contain arbitrary bytes; persisted diagnostics must
		// preserve their value when strict JSON decoding runs during recovery.
		message = strings.TrimSpace(strings.ToValidUTF8(err.Error(), "\ufffd"))
	}
	if message == "" {
		message = "unknown error"
	}
	if len(message) > maxFailureMessageBytes {
		message = message[:maxFailureMessageBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message = strings.TrimSpace(message)
	}
	failure, failureErr := NewFailure(kind, code, message)
	if failureErr != nil {
		panic(failureErr)
	}
	return failure
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

func (Failure) JSONSchemaAlias() any { return failureWire{} }
