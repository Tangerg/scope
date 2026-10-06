package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	documentfrontmatter "github.com/adrg/frontmatter"
	"go.yaml.in/yaml/v4"
	"golang.org/x/text/unicode/norm"
)

const frontmatterFence = "---"

var yamlFrontmatterFormat = documentfrontmatter.NewFormat(frontmatterFence, frontmatterFence, yaml.Unmarshal)

// Skill contains metadata and instructions. ResourceSource opens bundled files
// on demand; loading a skill does not read its resources.
type Skill struct {
	Frontmatter
	Instructions string
}

// Parse requires exact YAML fences so arbitrary Markdown cannot silently enter
// the skill discovery path as a partially populated manifest. The body after
// the closing fence remains untouched UTF-8 Markdown instruction content.
func Parse(content []byte) (*Skill, error) {
	content = bytes.TrimPrefix(content, []byte("\ufeff"))
	if !bytes.HasPrefix(content, []byte(frontmatterFence+"\n")) && !bytes.HasPrefix(content, []byte(frontmatterFence+"\r\n")) {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSkill, ErrNoFrontmatter)
	}

	var metadata Frontmatter
	body, err := documentfrontmatter.MustParse(bytes.NewReader(content), &metadata, yamlFrontmatterFormat)
	if errors.Is(err, documentfrontmatter.ErrNotFound) || errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSkill, ErrNoFrontmatter)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: parse frontmatter: %w", ErrInvalidSkill, err)
	}
	consumed := content[:len(content)-len(body)]
	if !hasExactClosingFence(consumed) {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSkill, ErrNoFrontmatter)
	}

	skill := &Skill{
		Frontmatter:  metadata,
		Instructions: string(body),
	}
	if err := skill.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSkill, err)
	}
	return skill, nil
}

func hasExactClosingFence(consumed []byte) bool {
	consumed = bytes.TrimSuffix(consumed, []byte("\n"))
	consumed = bytes.TrimSuffix(consumed, []byte("\r"))
	lineStart := bytes.LastIndexByte(consumed, '\n') + 1
	return bytes.Equal(consumed[lineStart:], []byte(frontmatterFence))
}

func (s *Skill) Validate() error {
	if s == nil {
		return ErrNilSkill
	}
	if !utf8.ValidString(s.Instructions) {
		return fmt.Errorf("%w: instructions must be valid UTF-8", ErrInvalidSkill)
	}
	return s.Frontmatter.Validate()
}

func (s *Skill) bindDirectoryName(name string) error {
	if norm.NFKC.String(s.Name) != norm.NFKC.String(name) {
		return fmt.Errorf("%w: frontmatter %q vs directory %q", ErrNameMismatch, s.Name, name)
	}
	s.Name = name
	return nil
}

// Summary supports discovery without loading a skill's instructions.
type Summary struct {
	Name        string
	Description string
}

func (s Summary) Validate() error {
	return (Frontmatter{Name: s.Name, Description: s.Description}).Validate()
}

func (s Skill) Summary() Summary {
	return Summary{Name: s.Name, Description: s.Description}
}
