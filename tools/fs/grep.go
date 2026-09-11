package fs

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// GrepRequest is the LLM-facing argument shape for the grep tool.
//
// Notes on pattern syntax: the underlying engine is ripgrep. Literal braces /
// brackets need escaping (`interface\{\}` to find `interface{}`).
// By default patterns match within a single line; set `multiline=true`
// for patterns that span newlines.
type GrepRequest struct {
	Pattern  string `json:"pattern" jsonschema:"minLength=1" jsonschema_description:"Regular expression in ripgrep syntax."`
	Path     string `json:"path,omitempty" jsonschema_description:"File or directory to search. Defaults to the workspace root."`
	FileGlob string `json:"file_glob,omitempty" jsonschema_description:"Optional file filter glob, such as **/*.go."`
	FileType string `json:"file_type,omitempty" jsonschema_description:"Optional ripgrep file type, such as go, ts, or rust."`

	IgnoreCase bool `json:"ignore_case,omitempty" jsonschema_description:"Case-insensitive search. Default false."`
	Multiline  bool `json:"multiline,omitempty" jsonschema_description:"Allow patterns to span line breaks. Default false. Requires ripgrep."`

	BeforeContextLines int `json:"before_context_lines,omitempty" jsonschema:"minimum=0,maximum=20" jsonschema_description:"Lines to include before each match. Defaults to 0 and cannot exceed 20."`
	AfterContextLines  int `json:"after_context_lines,omitempty" jsonschema:"minimum=0,maximum=20" jsonschema_description:"Lines to include after each match. Defaults to 0 and cannot exceed 20."`

	OutputMode GrepOutputMode `json:"output_mode,omitempty" jsonschema:"enum=content,enum=files_with_matches,enum=count" jsonschema_description:"Result projection: content (default), files_with_matches, or count."`

	MaxResults int `json:"max_results,omitempty" jsonschema:"minimum=1,maximum=1000" jsonschema_description:"Maximum result entries. Defaults to 250 and cannot exceed 1000."`
}

// GrepResponse is the LLM-facing return shape. Exactly one of
// lines / files / counts is populated based on the request's
// output_mode.
type GrepResponse struct {
	Lines     []GrepLine      `json:"lines,omitempty"`
	Files     []string        `json:"files,omitempty"`
	Counts    []GrepFileCount `json:"counts,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

var _ toolcontract.Tool = (*GrepTool)(nil)

// GrepTool is the model-facing adapter for the narrow Grepper port.
type GrepTool struct {
	executor Grepper
	typed    toolcontract.Func[GrepRequest, GrepResponse]
}

// NewGrepTool requires content-search authority explicitly.
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

// ConcurrencyPolicy declares independent calls to the concurrent-safe backend.
func (g *GrepTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}

func (g *GrepTool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return g.typed.Call(ctx, invocation)
}

func (g *GrepTool) grep(ctx context.Context, req GrepRequest) (GrepResponse, error) {
	res, err := g.executor.Grep(ctx, GrepInput{
		Pattern:       req.Pattern,
		Path:          req.Path,
		Glob:          req.FileGlob,
		FileType:      req.FileType,
		IgnoreCase:    req.IgnoreCase,
		Multiline:     req.Multiline,
		BeforeContext: req.BeforeContextLines,
		AfterContext:  req.AfterContextLines,
		OutputMode:    req.OutputMode,
		MaxResults:    req.MaxResults,
	})
	if err != nil {
		return GrepResponse{}, fmt.Errorf("fs.grep: %w", err)
	}
	return res, nil
}

// Unwrap exposes the typed input contract through tool decorators.
func (g *GrepTool) Unwrap() toolcontract.Tool { return g.typed }
