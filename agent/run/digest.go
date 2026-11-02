package run

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const digestPrefix = "sha256:"

var ErrInvalidDigest = errors.New("run: invalid digest")

// Digest is a canonical SHA-256 content identity. Its zero value is invalid.
type Digest struct{ identity }

func ParseDigest(value string) (Digest, error) {
	encoded, found := strings.CutPrefix(value, digestPrefix)
	if !found || len(encoded) != sha256.Size*2 || encoded != strings.ToLower(encoded) {
		return Digest{}, ErrInvalidDigest
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return Digest{}, fmt.Errorf("%w: %w", ErrInvalidDigest, err)
	}
	return Digest{identity{value: value}}, nil
}

// Hashes identify canonical bytes. Protocol owners must normalize equivalent
// JSON before hashing so representation details cannot create another identity.
func ComputeDigest(data []byte) Digest {
	sum := sha256.Sum256(data)
	return digestFromSum(sum[:])
}

func digestFromSum(sum []byte) Digest {
	return Digest{identity{value: digestPrefix + hex.EncodeToString(sum)}}
}

func (d Digest) hex() string {
	encoded, _ := strings.CutPrefix(d.value, digestPrefix)
	return encoded
}

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
