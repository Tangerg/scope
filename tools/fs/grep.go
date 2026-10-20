package fs

import (
	"context"
	"fmt"
	"strconv"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// GrepRequest patterns use ripgrep syntax. Matches stay within a single line
// unless Multiline is set.
type GrepRequest struct {
	Pattern  string `json:"pattern" jsonschema_description:"Regular expression in ripgrep syntax."`
	Path     string `json:"path,omitempty" jsonschema_description:"File or directory to search. Defaults to the workspace root."`
	FileGlob string `json:"file_glob,omitempty" jsonschema_description:"Optional file filter glob, such as **/*.go."`
	FileType string `json:"file_type,omitempty" jsonschema_description:"Optional ripgrep file type, such as go, ts, or rust."`

	IgnoreCase bool `json:"ignore_case,omitzero" jsonschema_description:"Case-insensitive search. Default false."`
	Multiline  bool `json:"multiline,omitzero" jsonschema_description:"Allow patterns to span line breaks. Default false. Requires ripgrep."`

	BeforeContextLines int `json:"before_context_lines,omitzero" jsonschema_description:"Lines to include before each match. Defaults to 0."`
	AfterContextLines  int `json:"after_context_lines,omitzero" jsonschema_description:"Lines to include after each match. Defaults to 0."`

	OutputMode GrepOutputMode `json:"output_mode,omitempty" jsonschema_description:"Result projection: content (default), files_with_matches, or count."`

	MaxResults int `json:"max_results,omitzero" jsonschema_description:"Maximum result entries. Defaults to 250."`
}

func (g GrepRequest) resultLimit() (int, error) {
	for _, lines := range []int{g.BeforeContextLines, g.AfterContextLines} {
		if lines < 0 || lines > maximumContextLines {
			return 0, fmt.Errorf("%w: grep context lines must be between 0 and %d", ErrInvalidInput, maximumContextLines)
		}
	}
	limit, err := searchResultLimit(g.MaxResults, defaultGrepMaxResults)
	if err != nil {
		return 0, err
	}
	if len(g.Pattern) < minimumPatternLength {
		return 0, ErrEmptyPattern
	}
	if g.FileGlob != "" {
		if err := validateGlobPattern(g.FileGlob); err != nil {
			return 0, err
		}
	}
	return limit, nil
}

func (g GrepRequest) ripgrepArguments(mode GrepOutputMode) []string {
	args := []string{"--json", "--no-config", "--no-follow"}
	if mode == GrepOutputContent {
		if g.BeforeContextLines > 0 {
			args = append(args, "--before-context", strconv.Itoa(g.BeforeContextLines))
		}
		if g.AfterContextLines > 0 {
			args = append(args, "--after-context", strconv.Itoa(g.AfterContextLines))
		}
	}
	if g.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if g.Multiline {
		args = append(args, "--multiline", "--multiline-dotall")
	}
	return append(args, "--regexp", g.Pattern, "--", "-")
}

// Exactly one of Lines, Files, or Counts is populated according to OutputMode.
type GrepResponse struct {
	Lines     []GrepLine      `json:"lines,omitempty"`
	Files     []string        `json:"files,omitempty"`
	Counts    []GrepFileCount `json:"counts,omitempty"`
	Truncated bool            `json:"truncated,omitzero"`
}

var _ toolcontract.Tool = (*GrepTool)(nil)

type GrepTool struct {
	executor Grepper
	typed    toolcontract.Func[GrepRequest, GrepResponse]
}

func NewGrepTool(executor Grepper) (*GrepTool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &GrepTool{executor: executor}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: "grep",
			Description: "Search file contents with a ripgrep regular expression. Use this instead of running grep or rg through shell. " +
				"Set output_mode=files_with_matches when only file paths are needed; use count for per-file match counts. " +
				"Set multiline=true only for patterns that span line breaks.",
		},
		t.grep,
	)
	if err != nil {
		return nil, fmt.Errorf("fs.NewGrepTool: %w", err)
	}
	t.typed = typed
	return t, nil
}

func (g *GrepTool) Definition() chat.ToolDefinition {
	return g.typed.Definition()
}

func (g *GrepTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}

func (g *GrepTool) Call(ctx context.Context, invocation toolcontract.Invocation) (toolcontract.Result, error) {
	return g.typed.Call(ctx, invocation)
}

func (g *GrepTool) grep(ctx context.Context, req GrepRequest) (GrepResponse, error) {
	res, err := g.executor.Grep(ctx, req)
	if err != nil {
		return GrepResponse{}, fmt.Errorf("fs.grep: %w", err)
	}
	return res, nil
}

func (g *GrepTool) Unwrap() toolcontract.Tool { return g.typed }

func (GrepRequest) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"pattern":              {MinLength: new(uint64(minimumPatternLength))},
		"before_context_lines": {Minimum: new(float64(0)), Maximum: new(float64(maximumContextLines))},
		"after_context_lines":  {Minimum: new(float64(0)), Maximum: new(float64(maximumContextLines))},
		"max_results":          {Minimum: new(float64(1)), Maximum: new(float64(maximumSearchResults))},
	}}
}
