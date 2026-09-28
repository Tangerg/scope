package fs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Reader is the read backend used by ReadTool. Implementations must support
// concurrent calls, including calls through other tools sharing the same
// backend. Private buffers, cursors, and caches remain the backend's concern.
// This promise permits ReadTool to advertise parallel reads; it is stronger
// than the absence of filesystem writes. LocalExecutor satisfies the contract.
type Reader interface {
	Read(ctx context.Context, in ReadInput) (ReadOutput, error)
}

// Writer is the narrow backend port consumed by WriteTool. On error the
// response retains acknowledged writes; an ordinary error does not establish
// whether a remote write committed or whether it is safe to repeat.
// ErrMutationRejected explicitly establishes that no mutation began.
type Writer interface {
	Write(ctx context.Context, request WriteRequest) (WriteResponse, error)
}

// Editor keeps read-modify-write atomic inside the filesystem authority owner.
// ErrMutationRejected reports a definite rejection with no mutation. Other errors
// do not establish the outcome and must not be treated as safe to retry.
// Responses retain acknowledged replacements even on error.
type Editor interface {
	Edit(ctx context.Context, request EditRequest) (EditResponse, error)
}

// PatchApplier validates a complete patch before mutation and reports every
// acknowledged file effect, including on error. A multi-file patch is not a
// filesystem transaction: commit failures can leave earlier changes applied.
// Implementations may create parent directories while committing files.
// Patch endpoints must follow [ApplyPatchTool.MutationPaths] so hosts can inspect
// the complete prospective write set before invoking the backend.
// A backend may return core/tool.Failure for an established unsuccessful outcome;
// that value owns the complete model-visible output. An ordinary error preserves
// uncertainty, and the Tool carries the response as core/tool.CallError evidence.
// ErrMutationRejected explicitly establishes rejection before any mutation.
type PatchApplier interface {
	ApplyPatch(ctx context.Context, request ApplyPatchRequest) (ApplyPatchResponse, error)
}

// Globber lets remote backends search paths without exposing directory walking
// as many tool calls. Like Reader, it must support concurrent backend calls.
type Globber interface {
	Glob(ctx context.Context, request GlobRequest) (GlobResponse, error)
}

// Grepper lets a backend own its content-search engine and filesystem boundary.
// Like Reader, it must support concurrent backend calls.
type Grepper interface {
	Grep(ctx context.Context, in GrepInput) (GrepResponse, error)
}

type ReadInput struct {
	Path           string
	Offset         int   // 0-based line offset; negative is clamped to 0
	Limit          int   // 0 = read to end of file
	MaxInputBytes  int64 // 0 = executor default
	MaxLineBytes   int   // 0 = executor default
	MaxOutputBytes int   // 0 = executor default
	PartialLine    bool  // admit a UTF-8 prefix when the output cap splits a line
}

func (r ReadInput) resolvedLimits() readLimits {
	return readLimits{
		inputBytes:  positiveOr(r.MaxInputBytes, defaultReadInputBytes),
		lineBytes:   positiveOr(r.MaxLineBytes, defaultReadLineBytes),
		outputBytes: positiveOr(r.MaxOutputBytes, defaultReadOutputBytes),
	}
}

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

func (e editOperation) apply(content, path string) (string, int, error) {
	if e.OldString == "" {
		return "", 0, errors.New("old_string must not be empty")
	}
	if e.OldString == e.NewString {
		return "", 0, errors.New("new_string must differ from old_string")
	}
	if strings.ContainsRune(e.NewString, 0) {
		return "", 0, ErrBinaryFile
	}
	occurrences := strings.Count(content, e.OldString)
	if occurrences == 0 {
		return "", 0, fmt.Errorf("old_string not found in %s", path)
	}
	if occurrences > 1 && !e.ReplaceAll {
		return "", 0, fmt.Errorf("old_string matches %d times in %s — set replace_all=true to confirm", occurrences, path)
	}
	return strings.ReplaceAll(content, e.OldString, e.NewString), occurrences, nil
}

type GrepOutputMode string

const (
	// GrepOutputContent returns structured matching and context lines.
	GrepOutputContent          GrepOutputMode = "content"
	GrepOutputFilesWithMatches GrepOutputMode = "files_with_matches"
	GrepOutputCount            GrepOutputMode = "count"
)

func (g GrepOutputMode) Normalize() (GrepOutputMode, error) {
	if g == "" {
		g = GrepOutputContent
	}
	if !g.Valid() {
		return "", fmt.Errorf("%w: invalid output_mode %q", ErrInvalidInput, g)
	}
	return g, nil
}

func (g GrepOutputMode) Valid() bool {
	switch g {
	case "", GrepOutputContent, GrepOutputFilesWithMatches, GrepOutputCount:
		return true
	default:
		return false
	}
}

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

func (g GrepInput) ripgrepArguments(mode GrepOutputMode) []string {
	args := []string{"--json", "--no-config", "--no-follow"}
	if mode == GrepOutputContent {
		before, after := g.contextLines()
		if before > 0 {
			args = append(args, "--before-context", strconv.Itoa(before))
		}
		if after > 0 {
			args = append(args, "--after-context", strconv.Itoa(after))
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

type GrepLineKind string

const (
	GrepLineMatch   GrepLineKind = "match"
	GrepLineContext GrepLineKind = "context"
)

func (g GrepLineKind) Valid() bool {
	return g == GrepLineMatch || g == GrepLineContext
}

func (g GrepLineKind) String() string { return string(g) }

type GrepLine struct {
	Path string       `json:"path"`
	Line int          `json:"line"` // 1-based
	Text string       `json:"text"`
	Kind GrepLineKind `json:"kind"`
}

type GrepFileCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}
