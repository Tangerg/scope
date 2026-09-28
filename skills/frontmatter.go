package skills

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	maxNameLen          = 64
	maxDescriptionLen   = 1024
	maxCompatibilityLen = 500
)

// Frontmatter is the YAML metadata block at the head of a SKILL.md file, as
// defined by the Agent Skills specification.
type Frontmatter struct {
	// Name is the unique skill identifier; it must match the skill's parent
	// directory name after Unicode NFKC normalization. Repository results use
	// the directory's exact spelling so the identifier can reopen its files.
	Name string `yaml:"name"`
	// Description states what the skill does and when to use it — the text an
	// agent reads to decide relevance. Required.
	Description string `yaml:"description"`
	// License names the license, or a bundled license file. Optional.
	License string `yaml:"license,omitempty"`
	// Compatibility states environment requirements (target product, system
	// packages, network access, ...). Optional.
	Compatibility string `yaml:"compatibility,omitempty"`
	// Metadata is an arbitrary string map for client-defined properties.
	Metadata map[string]string `yaml:"metadata,omitempty"`
	// AllowedTools is a space-separated list of pre-approved tools. Optional
	// and experimental; this package parses but does not enforce it.
	AllowedTools string `yaml:"allowed-tools,omitempty"`
}

// AllowedToolList splits the space-separated allowed-tools field into its
// entries. The field is experimental and advisory — this package neither
// interprets nor enforces it; the splitter is offered for callers that do.
func (f Frontmatter) AllowedToolList() []string {
	return strings.Fields(f.AllowedTools)
}

func (f Frontmatter) Validate() error {
	var errs []error

	if err := ValidateName(f.Name); err != nil {
		errs = append(errs, err)
	}

	// Description / Compatibility limits are in characters (the spec's
	// unit), so count runes — byte length over-counts non-ASCII text.
	descriptionLen := utf8.RuneCountInString(f.Description)
	switch {
	case strings.TrimSpace(f.Description) == "":
		errs = append(errs, ErrDescriptionEmpty)
	case descriptionLen > maxDescriptionLen:
		errs = append(errs, fmt.Errorf("%w: %d characters", ErrDescriptionTooLong, descriptionLen))
	}

	if compatibilityLen := utf8.RuneCountInString(f.Compatibility); compatibilityLen > maxCompatibilityLen {
		errs = append(errs, fmt.Errorf("%w: %d characters", ErrCompatibilityTooLong, compatibilityLen))
	}

	return errors.Join(errs...)
}

// ValidateName reports whether name satisfies the Agent Skills specification.
// It is useful at boundaries that only carry a skill identifier and should not
// need to fabricate a [Frontmatter] value to validate it.
// Validation compares Unicode NFKC characters without changing path spelling.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrNameEmpty
	}
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: %q", ErrNameInvalid, name)
	}
	normalized := norm.NFKC.String(name)
	switch {
	case utf8.RuneCountInString(normalized) > maxNameLen:
		return fmt.Errorf("%w: %d characters", ErrNameTooLong, utf8.RuneCountInString(normalized))
	case normalized != strings.ToLower(normalized), strings.HasPrefix(normalized, "-"), strings.HasSuffix(normalized, "-"), strings.Contains(normalized, "--"):
		return fmt.Errorf("%w: %q", ErrNameInvalid, name)
	}
	for _, character := range normalized {
		if character != '-' && !unicode.IsLetter(character) && !unicode.IsNumber(character) {
			return fmt.Errorf("%w: %q", ErrNameInvalid, name)
		}
	}
	return nil
}
