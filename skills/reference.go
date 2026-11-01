package skills

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"

	"github.com/samber/lo"
)

// Reference identifies the exact instructions or resource bytes actually used
// by a run. An empty Path names the instructions, excluding discovery metadata.
// A reference grants no access: resolving it always reopens the authorized source.
type Reference struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitzero"`
	Digest string `json:"digest"`
}

var ErrUnresolvedReference = errors.New("skills: exact content unavailable")

// NewReference binds the complete adopted content, never a page or truncated prefix.
func NewReference(name, path string, data []byte) (Reference, error) {
	if err := validateTarget(name, path); err != nil {
		return Reference{}, err
	}
	h := sha256.New()
	for _, field := range []string{name, path} {
		_, _ = h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(field))))
		_, _ = h.Write([]byte(field))
	}
	_, _ = h.Write(data)
	return Reference{Name: name, Path: path, Digest: hex.EncodeToString(h.Sum(nil))}, nil
}

func validateTarget(name, path string) error {
	if path != "" {
		return ValidateResourceRequest(name, path)
	}
	return ValidateName(name)
}

type ContentSource interface {
	ResourceOpener
	Load(context.Context, string) (*Skill, error)
}

// Resolve returns the complete, verified bytes or an error. A changed or missing
// binding matches ErrUnresolvedReference; transport and permission errors retain
// their original meaning. maxResourceBytes only bounds bundled resources.
func (r Reference) Resolve(ctx context.Context, source ContentSource, maxResourceBytes int64) ([]byte, error) {
	if err := validateTarget(r.Name, r.Path); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(r.Digest)
	if err != nil || len(raw) != sha256.Size {
		return nil, ErrUnresolvedReference
	}
	if lo.IsNil(source) {
		return nil, ErrNilSource
	}
	var data []byte
	if r.Path == "" {
		var s *Skill
		s, err = source.Load(ctx, r.Name)
		if err == nil {
			if err = s.Validate(); err == nil {
				if err = s.matchDirectoryName(r.Name); err == nil {
					data = []byte(s.Instructions)
				}
			}
		}
	} else {
		var truncated bool
		data, truncated, err = ReadResource(ctx, source, r.Name, r.Path, maxResourceBytes)
		if err == nil && truncated {
			return nil, ErrContentTooLarge
		}
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errors.Join(ErrUnresolvedReference, err)
		}
		return nil, err
	}
	got, err := NewReference(r.Name, r.Path, data)
	if err != nil {
		return nil, err
	}
	if got != r {
		return nil, ErrUnresolvedReference
	}
	return data, nil
}
