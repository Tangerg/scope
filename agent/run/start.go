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

func (s StartID) Valid() bool { return validIdentity(s.Scope) && validIdentity(s.Key) }
func (s StartID) Root() ProcessID {
	if !s.Valid() {
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
