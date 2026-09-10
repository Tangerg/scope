package etl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"

	"github.com/google/uuid"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// IDGenerator produces an identifier for a document. Implementations may
// derive identity from content or generate an unconditional random identity.
type IDGenerator interface {
	// Generate returns a non-blank identifier for one document without mutating
	// it. Content-addressed implementations must be deterministic; random
	// implementations must still honor ctx and reject nil documents.
	Generate(ctx context.Context, document *document.Document) (string, error)
}

// SHA256IDGenerator builds a content-addressable identifier from a document's
// text, media, and metadata. The existing ID is deliberately excluded so the
// digest remains stable before and after assignment.
type SHA256IDGenerator struct {
	salt []byte
}

// NewSHA256IDGenerator snapshots salt so identical documents retain stable IDs
// without sharing caller-owned bytes.
func NewSHA256IDGenerator(salt []byte) SHA256IDGenerator {
	return SHA256IDGenerator{salt: bytes.Clone(salt)}
}

// Generate hashes a JSON projection with ordered object members, minimal string
// escaping, and no whitespace. Number spellings are preserved exactly, avoiding
// precision loss through float64 normalization.
func (s SHA256IDGenerator) Generate(ctx context.Context, doc *document.Document) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if doc == nil {
		return "", ErrNilDocument
	}
	if err := doc.Validate(); err != nil {
		return "", fmt.Errorf("etl: generate SHA-256 ID: %w", err)
	}

	hasher := sha256.New()
	if len(s.salt) > 0 {
		_, _ = hasher.Write(s.salt)
		_, _ = hasher.Write([]byte{0})
	}
	projection := struct {
		Text     string       `json:"text,omitempty"`
		Media    *media.Media `json:"media,omitempty"`
		Metadata metadata.Map `json:"metadata,omitzero"`
	}{
		Text:     doc.Text,
		Media:    doc.Media,
		Metadata: doc.Metadata,
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("etl: encode document identity: %w", err)
	}
	identity := jsontext.Value(encoded)
	if err := identity.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return "", fmt.Errorf("etl: format document identity: %w", err)
	}
	_, _ = hasher.Write(identity)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// UUIDGenerator creates non-deterministic document identities when stable
// content identity is not required.
type UUIDGenerator struct{}

func (UUIDGenerator) Generate(ctx context.Context, doc *document.Document) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if doc == nil {
		return "", ErrNilDocument
	}
	if err := doc.Validate(); err != nil {
		return "", fmt.Errorf("etl: generate UUID: %w", err)
	}
	return uuid.NewString(), nil
}
