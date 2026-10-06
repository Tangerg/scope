package media

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/metadata"
)

var (
	ErrNilMedia      = errors.New("media: nil media")
	ErrInvalidMIME   = errors.New("media: invalid MIME type")
	ErrInvalidSource = errors.New("media: invalid source")
)

type SourceKind string

const (
	SourceBytes     SourceKind = "bytes"
	SourceURI       SourceKind = "uri"
	SourceReference SourceKind = "reference"
)

// Source is a tagged union. Kind selects exactly one of Bytes, URI, or
// Ref; all other fields must be empty.
type Source struct {
	Kind  SourceKind `json:"kind"`
	Bytes []byte     `json:"bytes,omitempty"`
	URI   string     `json:"uri,omitempty"`
	Ref   string     `json:"ref,omitempty"`
}

func (s Source) Validate() error {
	if !utf8.ValidString(s.URI) || !utf8.ValidString(s.Ref) {
		return fmt.Errorf("%w: URI and reference must be valid UTF-8", ErrInvalidSource)
	}
	switch s.Kind {
	case SourceBytes:
		if !s.carriesOnly(SourceBytes) {
			return fmt.Errorf("%w: kind %q requires non-empty bytes and no URI or reference", ErrInvalidSource, s.Kind)
		}
	case SourceURI:
		if !s.carriesOnly(SourceURI) {
			return fmt.Errorf("%w: kind %q requires a URI and no bytes or reference", ErrInvalidSource, s.Kind)
		}
		return s.validateURI()
	case SourceReference:
		if !s.carriesOnly(SourceReference) || strings.TrimSpace(s.Ref) == "" {
			return fmt.Errorf("%w: kind %q requires a reference and no bytes or URI", ErrInvalidSource, s.Kind)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidSource, s.Kind)
	}
	return nil
}

func (s Source) carriesOnly(kind SourceKind) bool {
	return (len(s.Bytes) != 0) == (kind == SourceBytes) &&
		(s.URI != "") == (kind == SourceURI) &&
		(s.Ref != "") == (kind == SourceReference)
}

func (s Source) validateURI() error {
	parsed, err := url.Parse(s.URI)
	if err != nil || parsed.Scheme == "" || (parsed.Opaque == "" && parsed.Host == "" && parsed.Path == "") {
		return fmt.Errorf("%w: %q is not an absolute URI", ErrInvalidSource, s.URI)
	}
	return nil
}

// Inline-byte construction snapshots the caller's buffer.
type Media struct {
	MIME     string       `json:"mime"`
	Source   Source       `json:"source"`
	ID       string       `json:"id,omitempty"`
	Name     string       `json:"name,omitempty"`
	Metadata metadata.Map `json:"metadata,omitzero"`
}

func (m *Media) Clone() *Media {
	if m == nil {
		return nil
	}
	clone := *m
	clone.Source.Bytes = slices.Clone(m.Source.Bytes)
	clone.Metadata = m.Metadata.Clone()
	return &clone
}

// NewBytes copies the payload so later caller mutation cannot change the media.
func NewBytes(mimeType string, data []byte) (*Media, error) {
	m := &Media{
		MIME: mimeType,
		Source: Source{
			Kind:  SourceBytes,
			Bytes: slices.Clone(data),
		},
		Metadata: metadata.Map{},
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// NewURI leaves content resolution to the provider or client; the URI must be reachable by it.
func NewURI(mimeType, uri string) (*Media, error) {
	m := &Media{
		MIME: mimeType,
		Source: Source{
			Kind: SourceURI,
			URI:  uri,
		},
		Metadata: metadata.Map{},
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// NewReference retains an opaque handle valid only with the provider that issued it.
func NewReference(mimeType, reference string) (*Media, error) {
	m := &Media{
		MIME: mimeType,
		Source: Source{
			Kind: SourceReference,
			Ref:  reference,
		},
		Metadata: metadata.Map{},
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Media) Validate() error {
	if m == nil {
		return ErrNilMedia
	}
	if !utf8.ValidString(m.MIME) {
		return fmt.Errorf("%w: must be valid UTF-8", ErrInvalidMIME)
	}
	if !utf8.ValidString(m.ID) || !utf8.ValidString(m.Name) {
		return errors.New("media: ID and name must be valid UTF-8")
	}
	mediaType, _, err := mime.ParseMediaType(m.MIME)
	if err != nil || !strings.Contains(mediaType, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidMIME, m.MIME)
	}
	if err := m.Source.Validate(); err != nil {
		return err
	}
	if err := m.Metadata.Validate(); err != nil {
		return fmt.Errorf("media: metadata: %w", err)
	}
	return nil
}

func (m *Media) Bytes() ([]byte, error) {
	if m == nil {
		return nil, ErrNilMedia
	}
	if m.Source.Kind != SourceBytes {
		return nil, fmt.Errorf("%w: source kind is %q, not %q", ErrInvalidSource, m.Source.Kind, SourceBytes)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return slices.Clone(m.Source.Bytes), nil
}

func (m *Media) URI() (string, error) {
	if m == nil {
		return "", ErrNilMedia
	}
	if m.Source.Kind != SourceURI {
		return "", fmt.Errorf("%w: source kind is %q, not %q", ErrInvalidSource, m.Source.Kind, SourceURI)
	}
	if err := m.Validate(); err != nil {
		return "", err
	}
	return m.Source.URI, nil
}

func (m *Media) Reference() (string, error) {
	if m == nil {
		return "", ErrNilMedia
	}
	if m.Source.Kind != SourceReference {
		return "", fmt.Errorf("%w: source kind is %q, not %q", ErrInvalidSource, m.Source.Kind, SourceReference)
	}
	if err := m.Validate(); err != nil {
		return "", err
	}
	return m.Source.Ref, nil
}

func (m Media) MarshalJSON() ([]byte, error) {
	if err := (&m).Validate(); err != nil {
		return nil, err
	}
	type wireMedia Media
	return jsonv2.Marshal(wireMedia(m), jsonv2.Deterministic(true))
}

func (m *Media) UnmarshalJSON(data []byte) error {
	if m == nil {
		return ErrNilMedia
	}
	type wireMedia Media
	var decoded wireMedia
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("media: decode: %w", err)
	}
	candidate := Media(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*m = candidate
	return nil
}
