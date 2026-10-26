package chatclient

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

var ErrInvalidTemplate = errors.New("chatclient: invalid template")

const (
	promptTemplateName       = "prompt"
	promptMissingKeyBehavior = "missingkey=error"
)

// Template is an immutable, parsed prompt template safe for concurrent use.
// Rendering fails on missing map keys instead of emitting placeholder text.
type Template struct {
	source   string
	compiled *template.Template
}

// ParseTemplate compiles an immutable prompt template from a non-empty source.
// It rejects a blank source and any parse error, and configures rendering to
// fail on a missing map key rather than emit placeholder text.
func ParseTemplate(source string) (*Template, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("%w: source is empty", ErrInvalidTemplate)
	}
	compiled, err := template.New(promptTemplateName).Option(promptMissingKeyBehavior).Parse(source)
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %w", ErrInvalidTemplate, err)
	}
	return &Template{source: source, compiled: compiled}, nil
}

func (t *Template) Source() string {
	if t == nil {
		return ""
	}
	return t.source
}

func (t *Template) Render(data any) (string, error) {
	if t == nil || t.compiled == nil {
		return "", fmt.Errorf("%w: nil template", ErrInvalidTemplate)
	}
	var rendered bytes.Buffer
	if err := t.compiled.Execute(&rendered, data); err != nil {
		return "", fmt.Errorf("%w: render: %w", ErrInvalidTemplate, err)
	}
	return rendered.String(), nil
}

// Require counts reachable static field references that retain the Render input's
// identity across scopes and called templates. It follows break and continue;
// conditional references count without promising that each Render evaluates them.
// Computed lookups and function-returned aliases are not inferred.
func (t *Template) Require(names ...string) error {
	if t == nil || t.compiled == nil {
		return fmt.Errorf("%w: nil template", ErrInvalidTemplate)
	}
	fields := templateFields{
		compiled: t.compiled,
		names:    make(map[string]struct{}),
		visited:  make(map[templateFieldCall]struct{}),
	}
	fields.inspect(templateFieldCall{name: t.compiled.Name(), root: true})

	var missing []string
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: required field name is empty", ErrInvalidTemplate)
		}
		if _, ok := fields.names[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	return fmt.Errorf("%w: missing fields: %s", ErrInvalidTemplate, strings.Join(missing, ", "))
}

func (t *Template) SystemMessage(data any) (chat.Message, error) {
	rendered, err := t.Render(data)
	if err != nil {
		return chat.Message{}, err
	}
	message := chat.NewSystemMessage(rendered)
	if err := message.Validate(); err != nil {
		return chat.Message{}, fmt.Errorf("%w: system message: %w", ErrInvalidTemplate, err)
	}
	return message, nil
}

func (t *Template) UserMessage(data any, attachments ...*media.Media) (chat.Message, error) {
	rendered, err := t.Render(data)
	if err != nil {
		return chat.Message{}, err
	}
	parts := make([]chat.Part, 0, 1+len(attachments))
	if rendered != "" {
		parts = append(parts, chat.NewTextPart(rendered))
	}
	for _, attachment := range attachments {
		parts = append(parts, chat.NewMediaPart(attachment))
	}
	message := chat.NewUserMessage(parts...)
	if err := message.Validate(); err != nil {
		return chat.Message{}, fmt.Errorf("%w: user message: %w", ErrInvalidTemplate, err)
	}
	return message, nil
}
