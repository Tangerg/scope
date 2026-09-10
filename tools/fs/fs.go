package fs

import (
	"cmp"
	"context"
)

// Each tool depends on the smallest backend capability it consumes. A backend
// may implement any combination of these ports; LocalExecutor implements all
// of them without forcing remote or policy-specific backends to grow unrelated
// methods.
type Reader interface {
	Read(ctx context.Context, in ReadInput) (ReadOutput, error)
}

// Writer is the narrow backend port consumed by WriteTool.
type Writer interface {
	Write(ctx context.Context, request WriteRequest) (WriteResponse, error)
}

// Editor keeps read-modify-write atomic inside the filesystem authority owner.
type Editor interface {
	Edit(ctx context.Context, request EditRequest) (EditResponse, error)
}

// PatchApplier validates a complete patch before mutation and reports every
// acknowledged file effect, including on error. A multi-file patch is not a
// filesystem transaction: commit failures can leave earlier changes applied.
// Implementations may create parent directories while committing files.
type PatchApplier interface {
	ApplyPatch(ctx context.Context, request ApplyPatchRequest) (ApplyPatchResponse, error)
}

// Globber lets remote backends search paths without exposing directory walking
// as many tool calls.
type Globber interface {
	Glob(ctx context.Context, request GlobRequest) (GlobResponse, error)
}

// Grepper lets a backend own its content-search engine and filesystem boundary.
type Grepper interface {
	Grep(ctx context.Context, in GrepInput) (GrepResponse, error)
}

// ReadInput is line-based. The executor handles binary detection and
// line windowing — the tool only forwards what the LLM asked for.
type ReadInput struct {
	Path           string
	Offset         int   // 0-based line offset; negative is clamped to 0
	Limit          int   // 0 = read to end of file
	MaxInputBytes  int64 // 0 = executor default
	MaxLineBytes   int   // 0 = executor default
	MaxOutputBytes int   // 0 = executor default
	PartialLine    bool  // admit a UTF-8 prefix when the output cap splits a line
}

// ReadOutput reports the admitted line window and whole-file size without
// leaking backend implementation details.
type ReadOutput struct {
	Content    string
	StartLine  int
	EndLine    int
	TotalLines int
	Truncated  bool
}

type editOperation struct {
	OldString  string
	NewString  string
	ReplaceAll bool
}

// GrepOutputMode controls what GrepResponse populates.
type GrepOutputMode string

const (
	// GrepOutputContent returns structured matching and context lines.
	GrepOutputContent GrepOutputMode = "content"
	// GrepOutputFilesWithMatches returns only paths containing a match.
	GrepOutputFilesWithMatches GrepOutputMode = "files_with_matches"
	// GrepOutputCount returns per-file match counts.
	GrepOutputCount GrepOutputMode = "count"
)

func (g GrepOutputMode) Resolve() GrepOutputMode {
	if g == "" {
		return GrepOutputContent
	}
	return g
}

func (g GrepOutputMode) Valid() bool {
	switch g.Resolve() {
	case GrepOutputContent, GrepOutputFilesWithMatches, GrepOutputCount:
		return true
	default:
		return false
	}
}

// GrepInput is the backend search contract after model-facing fields have been
// normalized.
type GrepInput struct {
	Pattern    string // regex
	Path       string // file or directory below the executor's authority root
	Glob       string // optional file filter ("*.go", "**/*.ts", ...)
	FileType   string // rg-style ("go", "ts", "rust", ...). Backend decides mapping.
	IgnoreCase bool
	Multiline  bool

	// Context is the symmetric "lines before AND after" shortcut.
	// BeforeContext / AfterContext override per-side when non-zero.
	Context       int
	BeforeContext int
	AfterContext  int

	// OutputMode picks the shape of GrepResponse. Its zero value resolves to
	// [GrepOutputContent].
	OutputMode GrepOutputMode

	MaxResults int
}

func (g GrepInput) contextLines() (before, after int) {
	return cmp.Or(g.BeforeContext, g.Context), cmp.Or(g.AfterContext, g.Context)
}

// GrepLineKind distinguishes a matching line from requested surrounding
// context.
type GrepLineKind string

const (
	// GrepLineMatch identifies content matched by the regular expression.
	GrepLineMatch GrepLineKind = "match"

	// GrepLineContext identifies surrounding content requested for a match.
	GrepLineContext GrepLineKind = "context"
)

func (g GrepLineKind) Valid() bool {
	return g == GrepLineMatch || g == GrepLineContext
}

func (g GrepLineKind) String() string { return string(g) }

// GrepLine is one structured ripgrep line event.
type GrepLine struct {
	Path string       `json:"path"`
	Line int          `json:"line"` // 1-based
	Text string       `json:"text"`
	Kind GrepLineKind `json:"kind"`
}

// GrepFileCount is one entry of the "count" output mode.
type GrepFileCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}
