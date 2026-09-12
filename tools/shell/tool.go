package shell

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
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
	Command   string `json:"command" jsonschema:"minLength=1" jsonschema_description:"Command line interpreted by the host-configured shell executor."`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"minimum=1,maximum=600000" jsonschema_description:"Hard execution timeout in milliseconds, from 1 to 600000. Omit for no timeout."`
}

// Response is the LLM-facing return shape. Stdout/stderr are strings
// (not []byte) because every consumer is a chat model.
type Response struct {
	Stdout               string `json:"stdout"`
	Stderr               string `json:"stderr"`
	ExitCode             int    `json:"exit_code"`
	CancellationObserved bool   `json:"cancellation_observed,omitempty"`
	Duration             string `json:"duration"`
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

// Config binds execution authority and the model-visible description.
type Config struct {
	Executor Executor
	// Description replaces the neutral default with the host's actual shell
	// semantics and tool-use policy. Empty selects the neutral description.
	Description string
}

// NewTool requires an executor because there is no safe default for running
// arbitrary commands; a package-level fallback would let a caller obtain shell
// access without ever stating where it should run.
func NewTool(config Config) (*Tool, error) {
	if lo.IsNil(config.Executor) {
		return nil, ErrNilExecutor
	}
	t := &Tool{executor: config.Executor}
	typed, err := toolcontract.NewFunc[Request, Response](
		toolcontract.FuncConfig{
			Name:        "shell",
			Description: cmp.Or(config.Description, "Execute a command through the host-configured shell executor. Returns stdout, stderr, exit code, and duration. Use timeout_ms when the command needs a hard deadline."),
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
	response := Response{
		Stdout:               string(res.Stdout),
		Stderr:               string(res.Stderr),
		ExitCode:             res.ExitCode,
		CancellationObserved: res.CancellationObserved,
		Duration:             res.Duration.String(),
	}
	if err == nil {
		return response, nil
	}
	cause := fmt.Errorf("shell.tool: run: %w", err)
	encoded, encodeErr := json.Marshal(response)
	if encodeErr != nil {
		return Response{}, errors.Join(cause, encodeErr)
	}
	output := chat.NewTextToolOutput(fmt.Sprintf("%s\nCaptured execution output: %s", cause, encoded))
	output.Details = encoded
	failure, failureErr := toolcontract.NewFailure(cause, output)
	if failureErr != nil {
		return Response{}, errors.Join(cause, failureErr)
	}
	return Response{}, failure
}

// Unwrap exposes the typed input contract through tool decorators.
func (t *Tool) Unwrap() toolcontract.Tool { return t.typed }
