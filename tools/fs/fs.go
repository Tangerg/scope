package fs

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Tangerg/scope/tools/textread"

	"github.com/Tangerg/scope/tools/content"
)

// Reader is the read backend used by ReadTool. Implementations must support
// concurrent calls, including calls through other tools sharing the backend;
// ReadTool advertises parallel reads on that promise. Reads, globs and searches
// never request mutations of application state; completed errors with no usable
// output are definite failures, while cancellation preserves uncertainty.
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
	Grep(ctx context.Context, request GrepRequest) (GrepResponse, error)
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
		inputBytes:  cmp.Or(r.MaxInputBytes, defaultReadInputBytes),
		lineBytes:   cmp.Or(r.MaxLineBytes, defaultReadLineBytes),
		outputBytes: cmp.Or(r.MaxOutputBytes, defaultReadOutputBytes),
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

func (e editOperation) validate() error {
	if e.OldString == "" {
		return fmt.Errorf("%w: old_string must not be empty", ErrInvalidInput)
	}
	if e.OldString == e.NewString {
		return fmt.Errorf("%w: new_string must differ from old_string", ErrInvalidInput)
	}
	for _, text := range []string{e.OldString, e.NewString} {
		if err := textread.Validate([]byte(text)); err != nil {
			return ErrBinaryFile
		}
	}
	return nil
}

func (e editOperation) apply(text, path string) (string, int, error) {
	if err := e.validate(); err != nil {
		return "", 0, err
	}
	occurrences := strings.Count(text, e.OldString)
	if occurrences == 0 {
		return "", 0, fmt.Errorf("old_string not found in %s", path)
	}
	if occurrences > 1 && !e.ReplaceAll {
		return "", 0, fmt.Errorf("old_string matches %d times in %s — set replace_all=true to confirm", occurrences, path)
	}
	return strings.ReplaceAll(text, e.OldString, e.NewString), occurrences, nil
}

type GrepOutputMode string

const (
	GrepOutputContent          GrepOutputMode = "content"
	GrepOutputFilesWithMatches GrepOutputMode = "files_with_matches"
	GrepOutputCount            GrepOutputMode = "count"
)

var grepOutputModeVocabulary = [...]GrepOutputMode{GrepOutputContent, GrepOutputFilesWithMatches, GrepOutputCount}

func (GrepOutputMode) JSONSchemaEnum() []string {
	values := make([]string, len(grepOutputModeVocabulary))
	for index, value := range grepOutputModeVocabulary {
		values[index] = string(value)
	}
	return values
}

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
	return g == "" || slices.Contains(grepOutputModeVocabulary[:], g)
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
	Path    string          `json:"path"`
	Line    int             `json:"line"` // 1-based
	Content content.Content `json:"content"`
	Kind    GrepLineKind    `json:"kind"`
}

type GrepFileCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}
