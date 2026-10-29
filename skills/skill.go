package skills

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
)

const frontmatterFence = "---"

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
	metadata, consumed, err := readFrontmatter(context.Background(), bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSkill, err)
	}
	return parseSkill(metadata, content[consumed:])
}

func parseSkill(header, body []byte) (*Skill, error) {
	var metadata Frontmatter
	if err := yaml.Unmarshal(header, &metadata); err != nil {
		return nil, fmt.Errorf("%w: parse frontmatter: %w", ErrInvalidSkill, err)
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

func readFrontmatter(ctx context.Context, reader io.Reader, maxBytes int64) ([]byte, int64, error) {
	limited := io.LimitReader(contextReader{ctx: ctx, reader: reader}, maxBytes+1)
	buffered := bufio.NewReaderSize(limited, int(min(maxBytes+1, 4096)))

	var metadata bytes.Buffer
	var bytesRead int64
	for lineNumber := 0; ; lineNumber++ {
		text, readErr := buffered.ReadString('\n')
		if contextErr := contextError(ctx, "read frontmatter"); contextErr != nil {
			return nil, 0, errors.Join(readErr, contextErr)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, 0, readErr
		}
		bytesRead += int64(len(text))
		if bytesRead > maxBytes {
			return nil, 0, fmt.Errorf("%w: frontmatter exceeds %d bytes", ErrContentTooLarge, maxBytes)
		}
		line := strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
		if lineNumber == 0 {
			if strings.TrimPrefix(line, "\ufeff") != frontmatterFence {
				return nil, 0, ErrNoFrontmatter
			}
		} else if line == frontmatterFence {
			return metadata.Bytes(), bytesRead, nil
		} else {
			metadata.WriteString(text)
		}
		if readErr != nil {
			return nil, 0, ErrNoFrontmatter
		}
	}
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

func (s *Skill) matchDirectoryName(name string) error {
	if s.Name != name {
		return fmt.Errorf("%w: frontmatter %q vs directory %q", ErrNameMismatch, s.Name, name)
	}
	return nil
}

// Summary supports discovery without loading a skill's instructions.
type Summary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s Summary) Validate() error {
	return (Frontmatter{Name: s.Name, Description: s.Description}).Validate()
}

func (s Skill) Summary() Summary {
	return Summary{Name: s.Name, Description: s.Description}
}
