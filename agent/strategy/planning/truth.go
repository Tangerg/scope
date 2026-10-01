package planning

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

const invalidEnumName = "invalid"

// Truth is the three-valued truth of one observed condition. TruthUnknown is
// not a synonym for TruthFalse: it means the current WorldState does not establish either
// known value. The zero value is invalid; callers must choose explicitly.
type Truth string

const (
	TruthUnknown Truth = "unknown"
	TruthFalse   Truth = "false"
	TruthTrue    Truth = "true"
)

func (t Truth) Valid() bool { return t == TruthUnknown || t == TruthFalse || t == TruthTrue }

func (t Truth) known() bool { return t == TruthFalse || t == TruthTrue }

func (t Truth) String() string {
	if !t.Valid() {
		return invalidEnumName
	}
	return string(t)
}

func (t Truth) MarshalJSON() ([]byte, error) {
	if !t.Valid() {
		return nil, fmt.Errorf("%w: truth value %q", ErrInvalidCondition, t)
	}
	return jsonv2.Marshal(t.String())
}

func (t *Truth) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil Truth receiver", ErrInvalidCondition)
	}
	encoded, err := jsonwire.Decode[string](data)
	if err != nil {
		return fmt.Errorf("%w: decode Truth: %w", ErrInvalidCondition, err)
	}
	decoded := Truth(encoded)
	if !decoded.Valid() {
		return fmt.Errorf("%w: unsupported truth %q", ErrInvalidCondition, encoded)
	}
	*t = decoded
	return nil
}
