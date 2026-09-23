package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	treeIncarnationIDPrefix    = "incarnation:"
	treeIncarnationRandomBytes = 16
)

var ErrInvalidTreeIncarnationID = errors.New("agent: invalid tree incarnation identity")

// TreeIncarnationID identifies the one active writer generation of a durable
// Process tree. Its zero value is invalid.
type TreeIncarnationID struct{ identity }

// parseTreeIncarnationID validates the canonical wire representation. No Agent
// boundary accepts a caller-built incarnation, so decoding serves UnmarshalText
// alone.
func parseTreeIncarnationID(value string) (TreeIncarnationID, error) {
	id, err := parseHexIdentity(value, treeIncarnationIDPrefix, treeIncarnationRandomBytes)
	if err != nil {
		return TreeIncarnationID{}, fmt.Errorf("%w: %w", ErrInvalidTreeIncarnationID, err)
	}
	return TreeIncarnationID{id}, nil
}

func newTreeIncarnationID() TreeIncarnationID {
	var random [treeIncarnationRandomBytes]byte
	rand.Read(random[:])
	return TreeIncarnationID{identity{value: treeIncarnationIDPrefix + hex.EncodeToString(random[:])}}
}

func (t TreeIncarnationID) MarshalText() ([]byte, error) {
	if !t.Valid() {
		return nil, ErrInvalidTreeIncarnationID
	}
	return []byte(t.value), nil
}

func (t *TreeIncarnationID) UnmarshalText(text []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidTreeIncarnationID)
	}
	value, err := parseTreeIncarnationID(string(text))
	if err != nil {
		return err
	}
	*t = value
	return nil
}
