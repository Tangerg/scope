package shell

import (
	"context"
	"fmt"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// Request is the LLM-facing argument shape. It is a strict subset of
// [Input] — environment, working directory, and streaming are
// executor-side concerns, not LLM knobs.
type Request struct {
	Command   string `json:"command" jsonschema:"minLength=1" jsonschema_description:"Shell command line run by /bin/sh -c."`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"minimum=1,maximum=600000" jsonschema_description:"Hard execution timeout in milliseconds, from 1 to 600000. Omit for no timeout."`
}

// Response is the LLM-facing return shape. Stdout/stderr are strings
// (not []byte) because every consumer is a chat model.
type Response struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Killed   bool   `json:"killed,omitempty"`
	Duration string `json:"duration"`
}

var _ toolcontract.Tool = (*Tool)(nil)

// Tool exposes shell execution to a model. It holds an [Executor] rather than
// running commands itself so the dangerous half — where a command runs, under
// what shell, with what limits — is chosen by the host at construction and
// cannot be influenced by the model's arguments.
type Tool struct {
	executor Executor
	typed    toolcontract.Func[Request, Response]
}

// NewTool requires an executor because there is no safe default for running
// arbitrary commands; a package-level fallback would let a caller obtain shell
// access without ever stating where it should run.
func NewTool(executor Executor) (*Tool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &Tool{executor: executor}
	typed, err := toolcontract.NewFunc[Request, Response](
		toolcontract.FuncConfig{
			Name: "shell",
			Description: "Execute a shell command via /bin/sh -c. Returns stdout, stderr, exit code, and duration. " +
				"Avoid using `find`, `grep`, `cat`, `head`, `tail`, `sed`, `awk` here — use the dedicated `glob`, `grep`, `read`, `edit` tools instead. Reserve `shell` for operations that genuinely need a shell (build commands, git, package managers, etc.). " +
				"Each invocation starts a fresh shell — `cd`, exported variables, and shell options do not persist between calls. Use timeout_ms only when the command needs a hard deadline.",
		},
		t.run,
	)
	if err != nil {
		return nil, fmt.Errorf("shell.NewTool: %w", err)
	}
	t.typed = typed
	return t, nil
}

func (t *Tool) Definition() chat.ToolDefinition {
	return t.typed.Definition()
}

func (t *Tool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return t.typed.Call(ctx, invocation)
}

func (t *Tool) run(ctx context.Context, req Request) (Response, error) {
	res, err := t.executor.Run(ctx, Input{
		Cmd:     req.Command,
		Timeout: time.Duration(req.TimeoutMS) * time.Millisecond,
	})
	if err != nil {
		return Response{}, fmt.Errorf("shell.tool: run: %w", err)
	}
	return Response{
		Stdout:   string(res.Stdout),
		Stderr:   string(res.Stderr),
		ExitCode: res.ExitCode,
		Killed:   res.Killed,
		Duration: res.Duration.String(),
	}, nil
}

// Unwrap exposes the typed input contract through tool decorators.
func (t *Tool) Unwrap() toolcontract.Tool { return t.typed }
