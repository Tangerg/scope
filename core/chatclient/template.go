package chatclient

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

// ErrInvalidTemplate identifies a prompt template that cannot render a valid
// request.
var ErrInvalidTemplate = errors.New("chatclient: invalid template")

// Template is an immutable, parsed prompt template safe for concurrent use.
// Parsing fails on missing map keys instead of emitting placeholder text;
// Require inspects the complete parse tree, including pipelines and branches.
// Root field references such as $.Query are recognized alongside .Query.
// Message projections validate the rendered protocol value and preserve media
// order without retaining per-render variables in Template.
type Template struct {
	source   string
	compiled *template.Template
}

// ParseTemplate rejects templates whose variable paths or functions could make
// rendering depend on ambient state.
func ParseTemplate(source string) (*Template, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("%w: source is empty", ErrInvalidTemplate)
	}
	compiled, err := template.New("prompt").Option("missingkey=error").Parse(source)
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

func (t *Template) Require(names ...string) error {
	if t == nil || t.compiled == nil {
		return fmt.Errorf("%w: nil template", ErrInvalidTemplate)
	}
	used := make(map[string]struct{})
	for _, associated := range t.compiled.Templates() {
		tree := associated.Tree
		if tree != nil && tree.Root != nil {
			collectTemplateFields(tree.Root, used)
		}
	}

	var missing []string
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: required field name is empty", ErrInvalidTemplate)
		}
		if _, ok := used[name]; !ok {
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

func collectTemplateFields(node parse.Node, fields map[string]struct{}) {
	if node == nil {
		return
	}
	switch value := node.(type) {
	case *parse.ListNode:
		for _, child := range value.Nodes {
			collectTemplateFields(child, fields)
		}
	case *parse.ActionNode:
		collectTemplateFields(value.Pipe, fields)
	case *parse.IfNode:
		collectBranchFields(value.Pipe, value.List, value.ElseList, fields)
	case *parse.RangeNode:
		collectBranchFields(value.Pipe, value.List, value.ElseList, fields)
	case *parse.WithNode:
		collectBranchFields(value.Pipe, value.List, value.ElseList, fields)
	case *parse.TemplateNode:
		if value.Pipe != nil {
			collectTemplateFields(value.Pipe, fields)
		}
	case *parse.PipeNode:
		for _, command := range value.Cmds {
			collectTemplateFields(command, fields)
		}
	case *parse.CommandNode:
		for _, argument := range value.Args {
			collectTemplateFields(argument, fields)
		}
	case *parse.FieldNode:
		if len(value.Ident) > 0 {
			fields[value.Ident[0]] = struct{}{}
		}
	case *parse.VariableNode:
		if len(value.Ident) > 1 && value.Ident[0] == "$" {
			fields[value.Ident[1]] = struct{}{}
		}
	case *parse.ChainNode:
		collectTemplateFields(value.Node, fields)
		if _, dot := value.Node.(*parse.DotNode); dot && len(value.Field) > 0 {
			fields[value.Field[0]] = struct{}{}
		}
	}
}

func collectBranchFields(pipe *parse.PipeNode, list, elseList *parse.ListNode, fields map[string]struct{}) {
	if pipe != nil {
		collectTemplateFields(pipe, fields)
	}
	if list != nil {
		collectTemplateFields(list, fields)
	}
	if elseList != nil {
		collectTemplateFields(elseList, fields)
	}
}
