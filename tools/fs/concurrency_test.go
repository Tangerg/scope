package fs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// concurrencyAware is the optional scheduling contract the tool loop discovers
// on a tool. It is declared here rather than imported so this package keeps
// depending only on what it uses.
type concurrencyAware interface {
	ConcurrencyPolicy() func(toolcontract.Invocation) (key string, concurrent bool)
}

func invocationFor(t *testing.T, executable toolcontract.Tool, arguments string) toolcontract.Invocation {
	t.Helper()
	binding, err := toolcontract.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{
		ID: "test-call", Name: binding.Contract().Definition().Name, Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

// Backends promise concurrent use; the adapters do not impose a resource key.
func TestReadOnlyToolsDeclareNoConflict(t *testing.T) {
	root := t.TempDir()
	executor := mustLocalExecutor(t, root)

	cases := map[string]struct {
		tool      toolcontract.Tool
		arguments string
	}{
		"read": {tool: mustReadTool(t, executor), arguments: `{"path":"a.txt"}`},
		"glob": {tool: mustGlobTool(t, executor), arguments: `{"pattern":"**/*.go"}`},
		"grep": {tool: mustGrepTool(t, executor), arguments: `{"pattern":"scope"}`},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			aware, ok := testCase.tool.(concurrencyAware)
			if !ok {
				t.Fatalf("%T does not declare a concurrency key", testCase.tool)
			}
			key, concurrent := aware.ConcurrencyPolicy()(invocationFor(t, testCase.tool, testCase.arguments))
			if !concurrent {
				t.Fatal("a read-only tool declared itself exclusive")
			}
			if key != "" {
				t.Fatalf("a read-only tool claimed the conflict key %q", key)
			}
		})
	}
}

type overlappingReader struct {
	active  atomic.Int32
	started chan string
	release <-chan struct{}
}

func (o *overlappingReader) Read(ctx context.Context, input ReadInput) (ReadOutput, error) {
	o.active.Add(1)
	defer o.active.Add(-1)
	o.started <- input.Path
	select {
	case <-o.release:
		return ReadOutput{Content: input.Path, EndLine: 1, TotalLines: 1}, nil
	case <-ctx.Done():
		return ReadOutput{}, ctx.Err()
	}
}

func TestReadToolUsesConcurrentReaderContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	reader := &overlappingReader{started: make(chan string, 2), release: release}
	executable, err := NewReadTool(reader)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := toolcontract.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "read", Name: "read", Arguments: `{"path":"file.txt"}`})
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 2)
	for range 2 {
		go func() {
			_, callErr := binding.Call(ctx, invocation)
			completed <- callErr
		}()
	}
	for range 2 {
		select {
		case <-reader.started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if reader.active.Load() != 2 {
		t.Fatal("declared reads did not reach the backend concurrently")
	}
	close(release)
	for range 2 {
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
	}
}

// Mutation adapters cannot infer resource identity or concurrency guarantees
// from an arbitrary Writer or Editor's path strings.
func TestMutatingToolsDoNotDeclareBackendConcurrency(t *testing.T) {
	executor := mustLocalExecutor(t, t.TempDir())
	for _, executable := range []toolcontract.Tool{mustEditTool(t, executor), mustWriteTool(t, executor)} {
		if _, ok := executable.(concurrencyAware); ok {
			t.Fatalf("%T declares concurrency without a backend contract", executable)
		}
	}
}

// TestGrepLineKindIsAClosedVocabulary keeps the structured grep event readable:
// a kind outside the pair would leave a consumer unable to tell a match from
// requested context.
func TestGrepLineKindIsAClosedVocabulary(t *testing.T) {
	for _, kind := range []GrepLineKind{GrepLineMatch, GrepLineContext} {
		if !kind.Valid() {
			t.Errorf("%q reports itself invalid", kind)
		}
		if kind.String() != string(kind) {
			t.Errorf("%q prints as %q", kind, kind.String())
		}
	}
	for _, kind := range []GrepLineKind{"", "unknown", "MATCH"} {
		if kind.Valid() {
			t.Errorf("%q reports itself valid", kind)
		}
	}
}

// TestReadLineNumberSurfacesTheOffendingLine is what turns an oversized-line
// failure into an actionable one: without the line number the caller only knows
// that some line in the file was too long.
func TestReadLineNumberSurfacesTheOffendingLine(t *testing.T) {
	err := &lineLimitError{path: "a.txt", line: 42, limit: 1024}
	if !errors.Is(err, ErrLineTooLarge) {
		t.Fatalf("error does not unwrap to ErrLineTooLarge: %v", err)
	}
	if got := ReadLineNumber(err); got != 42 {
		t.Fatalf("ReadLineNumber = %d, want 42", got)
	}
	if message := err.Error(); message == "" {
		t.Fatal("the error has no message")
	}

	if got := ReadLineNumber(ErrFileTooLarge); got != 0 {
		t.Fatalf("ReadLineNumber on a non-line error = %d, want 0", got)
	}
	if got := ReadLineNumber(nil); got != 0 {
		t.Fatalf("ReadLineNumber(nil) = %d, want 0", got)
	}
}
