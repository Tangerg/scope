package shell

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/content"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

// Environment, working directory, and streaming remain executor-owned.
type Request struct {
	Command   string `json:"command" jsonschema:"minLength=1" jsonschema_description:"Command line interpreted by the host-configured shell executor."`
	TimeoutMS int    `json:"timeout_ms,omitzero" jsonschema:"minimum=1,maximum=600000" jsonschema_description:"Hard execution timeout in milliseconds, from 1 to 600000. Omit for no timeout."`
}

// Captured bytes remain lossless for binary output and truncated UTF-8 text.
type Response struct {
	Stdout               content.Content `json:"stdout"`
	Stderr               content.Content `json:"stderr"`
	StdoutTruncated      bool            `json:"stdout_truncated,omitzero"`
	StderrTruncated      bool            `json:"stderr_truncated,omitzero"`
	ExitCode             int             `json:"exit_code"`
	CancellationObserved bool            `json:"cancellation_observed,omitzero"`
	Duration             string          `json:"duration"`
}

var _ toolcontract.Tool = (*Tool)(nil)

// Tool retains Host-selected execution authority; model arguments cannot
// change the shell, working directory, or process policy.
type Tool struct {
	executor Executor
	typed    toolcontract.Func[Request, Response]
}

type Config struct {
	Executor Executor
	// Description replaces the neutral default with the host's actual shell
	// semantics and tool-use policy. Empty selects the neutral description.
	Description string
}

func NewTool(config Config) (*Tool, error) {
	if lo.IsNil(config.Executor) {
		return nil, ErrNilExecutor
	}
	t := &Tool{executor: config.Executor}
	typed, err := toolcontract.NewFunc[Request, Response](
		toolcontract.FuncConfig{
			Name:        "shell",
			Description: cmp.Or(config.Description, "Execute a command through the host-configured shell executor. Returns stdout, stderr, exit code, and duration. Each stream contains encoding and data: utf8 is readable text; base64 preserves non-UTF8 bytes. Use timeout_ms when the command needs a hard deadline."),
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
		Stdout:               content.New(res.Stdout),
		Stderr:               content.New(res.Stderr),
		StdoutTruncated:      res.StdoutTruncated,
		StderrTruncated:      res.StderrTruncated,
		ExitCode:             res.ExitCode,
		CancellationObserved: res.CancellationObserved,
		Duration:             res.Duration.String(),
	}
	if err == nil {
		return response, nil
	}
	_, definite := errors.AsType[*toolcontract.Failure](err)
	_, observed := errors.AsType[*toolcontract.CallError](err)
	if !definite && !observed && (errors.Is(err, ErrEmptyCommand) || errors.Is(err, ErrInvalidInput)) {
		cause := fmt.Errorf("shell.tool: run: %w", err)
		failure, failureErr := toolcontract.NewFailure(toolcontract.FailureConfig{
			Kind: toolcontract.FailureKindFailed, Cause: cause, Output: chat.NewTextToolOutput(cause.Error()),
		})
		if failureErr != nil {
			return Response{}, errors.Join(cause, failureErr)
		}
		return Response{}, failure
	}
	return Response{}, toolresult.WithEvidence("shell.tool: run", response, err)
}

func (t *Tool) Unwrap() toolcontract.Tool { return t.typed }
