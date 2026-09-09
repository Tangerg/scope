package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const digestPrefix = "sha256:"

var ErrInvalidDigest = errors.New("agent: invalid digest")

// Digest is a canonical SHA-256 content identity. Its zero value is invalid.
type Digest struct {
	value string
}

// ParseDigest validates a canonical sha256:<lowercase-hex> identity.
func ParseDigest(value string) (Digest, error) {
	encoded, ok := strings.CutPrefix(value, digestPrefix)
	if !ok || len(encoded) != sha256.Size*2 || encoded != strings.ToLower(encoded) {
		return Digest{}, ErrInvalidDigest
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return Digest{}, fmt.Errorf("%w: %w", ErrInvalidDigest, err)
	}
	return Digest{value: value}, nil
}

// ComputeDigest returns the canonical SHA-256 identity of data. Callers that
// assemble a Deployment use it for reproducible implementation artifacts and
// canonical frozen configuration bytes.
func ComputeDigest(data []byte) Digest { return digestBytes(data) }

func digestBytes(data []byte) Digest {
	sum := sha256.Sum256(data)
	return Digest{value: digestPrefix + hex.EncodeToString(sum[:])}
}

func (d Digest) hex() string {
	encoded, _ := strings.CutPrefix(d.value, digestPrefix)
	return encoded
}

func (d Digest) String() string { return d.value }

func (d Digest) Valid() bool {
	_, err := ParseDigest(d.value)
	return err == nil
}

func (Digest) JSONSchemaAlias() any { return "" }

func (d Digest) MarshalText() ([]byte, error) {
	if !d.Valid() {
		return nil, ErrInvalidDigest
	}
	return []byte(d.value), nil
}

func (d *Digest) UnmarshalText(text []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDigest)
	}
	value, err := ParseDigest(string(text))
	if err != nil {
		return err
	}
	*d = value
	return nil
}
