package managed

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"time"

	"github.com/Tangerg/scope/agent/run"
)

// The issuer alone chooses the target. All request-specific authority is
// authenticated as associated data, not copied into a caller-editable payload.
type issuer struct {
	cipher   cipher.AEAD
	lifetime time.Duration
}
type target struct {
	Root    run.ProcessID
	Owner   run.ProcessID
	Wait    run.WaitID
	Expires time.Time
}

func newIssuer(key [32]byte, lifetime time.Duration) (issuer, error) {
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
func (i issuer) seal(t target, auth []byte) (string, error) {
	t.Expires = time.Now().Add(i.lifetime).UTC()
	raw, err := jsonv2.Marshal(t)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(i.cipher.Seal(nil, nil, raw, auth)), nil
}
func (i issuer) open(token string, auth []byte) (target, error) {
	var t target
	if len(token) == 0 || len(token) > 8192 {
		return t, errContinuation
	}
	buf, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return t, errContinuation
	}
	raw, err := i.cipher.Open(nil, nil, buf, auth)
	if err != nil {
		return t, errContinuation
	}
	if err = jsonv2.Unmarshal(raw, &t); err != nil || !t.Root.Valid() || !t.Owner.Valid() || !t.Wait.Valid() || !time.Now().Before(t.Expires) {
		return target{}, errContinuation
	}
	return t, nil
}
