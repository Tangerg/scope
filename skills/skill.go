package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	documentfrontmatter "github.com/adrg/frontmatter"
	"go.yaml.in/yaml/v4"
)

const frontmatterFence = "---"

var yamlFrontmatterFormat = documentfrontmatter.NewFormat(frontmatterFence, frontmatterFence, yaml.Unmarshal)

// Skill is a fully loaded skill: its frontmatter metadata plus the Markdown
// instruction body. Bundled resource files (references/, assets/, scripts/)
// are not loaded here — they are opened on demand via [ResourceSource],
// the third level of progressive disclosure.
type Skill struct {
	Frontmatter
	Instructions string
}

// Parse requires exact YAML fences so arbitrary Markdown cannot silently enter
// the skill discovery path as a partially populated manifest. The body after
// the closing fence remains untouched Markdown instruction content.
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
	return s.Frontmatter.Validate()
}

// Summary is the metadata view — just enough for an agent to decide whether a
// skill is relevant without loading its instructions (progressive-disclosure
// level 1).
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
