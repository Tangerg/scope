package chat

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidCitation = errors.New("chat: invalid citation")

// CitationSourceKind distinguishes an absolute URI from an opaque source
// reference whose interpretation remains with the provider or host.
type CitationSourceKind string

const (
	CitationSourceURI       CitationSourceKind = "uri"
	CitationSourceReference CitationSourceKind = "reference"
)

func (c CitationSourceKind) Valid() bool {
	return c == CitationSourceURI || c == CitationSourceReference
}

// CitationSource identifies cited material without adopting a provider's
// location taxonomy. Provider-native page, block, and character coordinates
// remain in the preserved native response. URI sources may use any scheme,
// including file and urn, and may include a fragment. They require a non-empty
// target and contain no whitespace. Validation identifies material; it never
// resolves the URI or grants permission to access it.
type CitationSource struct {
	Kind  CitationSourceKind `json:"kind"`
	Value string             `json:"value"`
}

func (c CitationSource) Validate() error {
	if !utf8.ValidString(c.Value) {
		return fmt.Errorf("%w: source value must be valid UTF-8", ErrInvalidCitation)
	}
	if !c.Kind.Valid() {
		return fmt.Errorf("%w: unknown source kind %q", ErrInvalidCitation, c.Kind)
	}
	if strings.TrimSpace(c.Value) == "" || strings.TrimSpace(c.Value) != c.Value {
		return fmt.Errorf("%w: source value must be non-empty without surrounding whitespace", ErrInvalidCitation)
	}
	if c.Kind == CitationSourceURI {
		parsed, err := url.Parse(c.Value)
		if err != nil || !parsed.IsAbs() || (parsed.Opaque == "" && parsed.Host == "" && parsed.Path == "") || strings.ContainsFunc(c.Value, unicode.IsSpace) {
			return fmt.Errorf("%w: source URI %q must be absolute with a non-empty target and no whitespace", ErrInvalidCitation, c.Value)
		}
	}
	return nil
}

// Citation is the portable identity and quoted evidence attached to a text
// part. Exact provider coordinates remain available through response metadata.
type Citation struct {
	Source CitationSource `json:"source"`
	Title  string         `json:"title,omitempty"`
	Quote  string         `json:"quote,omitempty"`
}

func (c Citation) Validate() error {
	if err := c.Source.Validate(); err != nil {
		return err
	}
	if !utf8.ValidString(c.Title) || !utf8.ValidString(c.Quote) {
		return fmt.Errorf("%w: title and quote must be valid UTF-8", ErrInvalidCitation)
	}
	if strings.TrimSpace(c.Title) != c.Title {
		return fmt.Errorf("%w: title must not have surrounding whitespace", ErrInvalidCitation)
	}
	return nil
}
