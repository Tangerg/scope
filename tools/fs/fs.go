package fs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Reader is the read backend used by ReadTool. Implementations must support
// concurrent calls, including calls through other tools sharing the backend;
// ReadTool advertises parallel reads on that promise.
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
// Patch endpoints must follow [ApplyPatchTool.MutationPaths] so hosts can inspect
// the complete prospective write set before invoking the backend.
// A core/tool.Failure owns the complete model-visible output of an established
// unsuccessful outcome; ErrMutationRejected establishes rejection before any
// mutation; any other error preserves uncertainty.
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

func (r ReadInput) validate() error {
	if r.Limit < 0 || r.MaxInputBytes < 0 || r.MaxLineBytes < 0 || r.MaxOutputBytes < 0 {
		return fmt.Errorf("%w: read limits must not be negative", ErrInvalidInput)
	}
	return nil
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

	BeforeContext int
	AfterContext  int

	OutputMode GrepOutputMode // zero resolves to GrepOutputContent
	MaxResults int
}

func (g GrepInput) resultLimit() (int, error) {
	for _, lines := range []int{g.BeforeContext, g.AfterContext} {
		if lines < 0 || lines > maximumContextLines {
			return 0, fmt.Errorf("%w: grep context lines must be between 0 and %d", ErrInvalidInput, maximumContextLines)
		}
	}
	limit, err := searchResultLimit(g.MaxResults, defaultGrepMaxResults)
	if err != nil {
		return 0, err
	}
	if g.Pattern == "" {
		return 0, ErrEmptyPattern
	}
	return limit, nil
}

func (g GrepInput) ripgrepArguments(mode GrepOutputMode) []string {
	args := []string{"--json", "--no-config", "--no-follow"}
	if mode == GrepOutputContent {
		if g.BeforeContext > 0 {
			args = append(args, "--before-context", strconv.Itoa(g.BeforeContext))
		}
		if g.AfterContext > 0 {
			args = append(args, "--after-context", strconv.Itoa(g.AfterContext))
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
