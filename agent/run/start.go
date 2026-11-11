package run

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrInvalidStart = errors.New("run: invalid start identity")

type StartID struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
}

// Valid reports whether a Host may submit the identity. The foreground scope
// belongs to Runtime.Run, whose random keys must not collide with Host keys.
func (s StartID) Valid() bool { return s.wellFormed() && s.Scope != foregroundStartScope }

func (s StartID) wellFormed() bool { return validIdentity(s.Scope) && validIdentity(s.Key) }

func (s StartID) Root() ProcessID {
	if !s.wellFormed() {
		return ProcessID{}
	}
	payload, _ := EncodePayload(s)
	return ProcessID{path: ComputeDigest(payload.JSON()).hex()}
}

type StartRequest[I any] struct {
	ID    StartID
	Input I
}

type StartConflictError struct {
	Root   ProcessID
	Intent Digest
}

func (s *StartConflictError) Error() string {
	return fmt.Sprintf("run: start for %s conflicts with committed intent %s", s.Root, s.Intent)
}

const foregroundStartScope = "run"
const foregroundStartRandomBytes = 16

func foregroundStartID() StartID {
	var random [foregroundStartRandomBytes]byte
	_, _ = rand.Read(random[:])
	return StartID{Scope: foregroundStartScope, Key: hex.EncodeToString(random[:])}
}
