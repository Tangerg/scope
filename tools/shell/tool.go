package shell

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/jsonschema"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/content"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

const toolName = "shell"

const maximumToolTimeout = 10 * time.Minute

// Environment, working directory, and streaming remain executor-owned.
type Request struct {
	Command   string `json:"command" jsonschema:"minLength=1" jsonschema_description:"Command line interpreted by the host-configured shell executor."`
	TimeoutMS int    `json:"timeout_ms,omitzero" jsonschema_description:"Hard execution timeout in milliseconds, within the tool's supported limit. Omit for no timeout."`
}

func (r Request) Validate() error {
	if r.TimeoutMS < 0 || int64(r.TimeoutMS) > maximumToolTimeout.Milliseconds() {
		return fmt.Errorf("%w: timeout must be between zero and %d milliseconds", ErrInvalidInput, maximumToolTimeout.Milliseconds())
	}
	return r.input().Validate()
}

func (r Request) input() Input {
	return Input{Cmd: r.Command, Timeout: time.Duration(r.TimeoutMS) * time.Millisecond}
}

func (Request) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"timeout_ms": {Minimum: new(float64(1)), Maximum: new(float64(maximumToolTimeout.Milliseconds()))},
	}}
}

// Captured bytes remain lossless for binary output and truncated UTF-8 text.
type Response struct {
	Stdout               content.Content `json:"stdout"`
	Stderr               content.Content `json:"stderr"`
	StdoutDroppedBytes   uint64          `json:"stdout_dropped_bytes,omitzero"`
	StderrDroppedBytes   uint64          `json:"stderr_dropped_bytes,omitzero"`
	ExitCode             int             `json:"exit_code"`
	CancellationObserved bool            `json:"cancellation_observed,omitzero"`
	Duration             string          `json:"duration"`
}

var _ toolcontract.Tool = (*Tool)(nil)

// shellFunc is embedded under an unexported name so the tool keeps its
// Func private while promoting the Tool methods.
type shellFunc = toolcontract.Func[Request, Response]

// Tool retains Host-selected execution authority; model arguments cannot
// change the shell, working directory, or process policy.
type Tool struct {
	shellFunc
	executor Executor
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
			Name:        toolName,
			Description: cmp.Or(config.Description, "Execute a command through the host-configured shell executor. Returns stdout, stderr, exit code, and duration. Each stream contains encoding and data: utf8 is readable text; base64 preserves non-UTF8 bytes. Dropped byte counts report omitted bytes; only the raw prefix is available. Use timeout_ms when the command needs a hard deadline."),
		},
		t.run,
	)
	if err != nil {
		return nil, fmt.Errorf("shell.NewTool: %w", err)
	}
	t.shellFunc = typed
	return t, nil
}

func (t *Tool) run(ctx context.Context, req Request) (Response, error) {
	res, err := t.executor.Run(ctx, req.input())
	response := Response{
		Stdout:               content.New(res.Stdout),
		Stderr:               content.New(res.Stderr),
		StdoutDroppedBytes:   res.StdoutDroppedBytes,
		StderrDroppedBytes:   res.StderrDroppedBytes,
		ExitCode:             res.ExitCode,
		CancellationObserved: res.CancellationObserved,
		Duration:             res.Duration.String(),
	}
	if err == nil {
		return response, nil
	}
	_, containsFailure := errors.AsType[*toolcontract.Failure](err)
	_, containsEvidence := errors.AsType[*toolcontract.CallError](err)
	if !containsFailure && !containsEvidence && (errors.Is(err, ErrEmptyCommand) || errors.Is(err, ErrInvalidInput)) {
		cause := fmt.Errorf("shell.tool: run: %w", err)
		return Response{}, toolresult.Correctable(cause.Error(), cause)
	}
	return Response{}, toolresult.WithEvidence("shell.tool: run", response, err)
}
