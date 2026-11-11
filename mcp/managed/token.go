package managed

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"time"

	"github.com/Tangerg/scope/agent/run"
)

const (
	resumeKeyBytes      = 32
	maximumTokenBytes   = 8 << 10
	authorizationDomain = "scope.mcp.managed"
)

type callAuthority struct {
	Domain, Program, Name, Native, Session string
	Start                                  run.StartID
	Arguments                              [sha256.Size]byte
}

// The issuer alone chooses the target. All request-specific authority is
// authenticated as associated data, not copied into a caller-editable payload.
type issuer struct {
	cipher   cipher.AEAD
	lifetime time.Duration
}

type target struct {
	Owner run.ProcessID
	Wait  run.WaitKey
}

// A process path grows with tree depth. Its digest keeps the opaque capability
// bounded; the committed run still owns the address used to answer.
type continuation struct {
	OwnerHash [sha256.Size]byte
	Wait      run.WaitKey
	Expires   time.Time
}

func (c continuation) resolve(ctx context.Context, runtime *run.Runtime, head run.TreeInspection) (target, error) {
	for _, process := range head.Processes {
		if c.targets(process.ID) {
			return target{Owner: process.ID, Wait: c.Wait}, nil
		}
	}
	// Reaping removes completed children from the recovery cut, but retries
	// still address their committed answers for the lifetime of the full log.
	history, err := runtime.History(ctx, head.Root)
	if err != nil {
		return target{}, errUnavailable
	}
	for _, entry := range history.Entries {
		if started, ok := entry.Record.(run.Started); ok && c.targets(started.Process) {
			return target{Owner: started.Process, Wait: c.Wait}, nil
		}
	}
	return target{}, errContinuation
}

func (c continuation) targets(owner run.ProcessID) bool {
	return sha256.Sum256([]byte(owner.String())) == c.OwnerHash
}

func newIssuer(key [resumeKeyBytes]byte, lifetime time.Duration) (issuer, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return issuer{}, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return issuer{}, err
	}
	return issuer{cipher: aead, lifetime: lifetime}, nil
}

// seal issues a token for a target projected from a committed wait, whose
// owner and key the Runtime already admitted.
func (i issuer) seal(t target, auth []byte) (string, error) {
	raw, err := jsonv2.Marshal(continuation{
		OwnerHash: sha256.Sum256([]byte(t.Owner.String())),
		Wait:      t.Wait,
		Expires:   time.Now().Add(i.lifetime).UTC(),
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(i.cipher.Seal(nil, nil, raw, auth)), nil
}

func (i issuer) open(token string, auth []byte) (continuation, error) {
	var c continuation
	if len(token) == 0 || len(token) > maximumTokenBytes {
		return c, errContinuation
	}
	buf, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return c, errContinuation
	}
	raw, err := i.cipher.Open(nil, nil, buf, auth)
	if err != nil {
		return c, errContinuation
	}
	if err = jsonv2.Unmarshal(raw, &c, jsonv2.RejectUnknownMembers(true)); err != nil || !c.Wait.Valid() || !time.Now().Before(c.Expires) {
		return continuation{}, errContinuation
	}
	return c, nil
}

func (t target) pendingIn(head run.TreeInspection) bool {
	process, ok := head.Process(t.Owner)
	if !ok {
		return false
	}
	for _, wait := range process.Waits {
		if wait.Key == t.Wait {
			return true
		}
	}
	return false
}
