package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
)

var (
	ErrInvalidTool       = errors.New("tool: invalid tool")
	ErrInvalidInvocation = errors.New("tool: invalid invocation")
)

// Tool is the minimal executable capability used by model-driven runtimes.
// Definition returns an independent snapshot safe to expose to a model. Call
// receives only an Invocation promoted by its exact frozen [Contract].
//
// Tool assigns no control-flow meaning to errors. Retry, pause, abort, and
// ordinary error feedback belong to the runtime driving the tool.
type Tool interface {
	// Definition returns a detached, valid schema snapshot. Callers may expose or
	// mutate the returned value without changing subsequent calls or execution.
	Definition() chat.ToolDefinition
	// Call executes one schema-validated invocation. Implementations still own
	// capability-specific semantic validation. Ordinary failure is returned as
	// error without assigning retry or control-flow meaning. Implementations must
	// honor ctx and must not retain the invocation or its arguments.
	// On error, the returned output is not consumed; use [Failure] to preserve
	// complete failure content, including acknowledged partial effects.
	Call(ctx context.Context, invocation Invocation) (chat.ToolOutput, error)
}

type contractState struct {
	definition chat.ToolDefinition
	input      corejsonschema.Schema
	validate   func([]byte) error
}

// Contract is the immutable trust boundary between an untrusted [chat.ToolCall]
// and a validated Invocation. It holds the frozen definition and compiled input
// schema and independent input validator without retaining an executable Tool.
// A Contract obtained from
// [Binding.Contract] is safe for concurrent use independently of the Tool.
type Contract struct {
	state *contractState
}

// Binding associates one immutable Contract with the Tool that executes it.
// Calls may run concurrently when the underlying Tool supports concurrent use.
type Binding struct {
	contract   Contract
	executable Tool
}

// Bind freezes and validates executable. Definition is read exactly once.
func Bind(executable Tool) (Binding, error) {
	if lo.IsNil(executable) {
		return Binding{}, fmt.Errorf("%w: tool is nil", ErrInvalidTool)
	}
	definition := executable.Definition()
	if err := definition.Validate(); err != nil {
		return Binding{}, fmt.Errorf("%w: definition: %w", ErrInvalidTool, err)
	}
	input, err := corejsonschema.Parse(definition.InputSchema)
	if err != nil {
		return Binding{}, fmt.Errorf("%w: input schema: %w", ErrInvalidTool, err)
	}
	validator, err := inputValidator(executable)
	if err != nil {
		return Binding{}, fmt.Errorf("%w: input validation: %w", ErrInvalidTool, err)
	}
	return Binding{
		contract:   Contract{state: &contractState{definition: definition.Clone(), input: input, validate: validator}},
		executable: executable,
	}, nil
}

// Contract returns the exact frozen contract used to prepare this Binding's
// invocations. Retaining it does not retain the executable Tool.
func (b Binding) Contract() Contract { return b.contract }

// Definition returns an independent snapshot of the frozen definition.
func (c Contract) Definition() chat.ToolDefinition {
	if c.state == nil {
		return chat.ToolDefinition{}
	}
	return c.state.definition.Clone()
}

// Invocation is a complete JSON object admitted by one exact frozen Tool
// contract. Its fields are intentionally private: only Contract.Prepare can
// promote an untrusted model proposal into an executable invocation.
type Invocation struct {
	contract  *contractState
	arguments []byte
}

// Prepare validates identity, RFC 7493 JSON syntax, the frozen input schema,
// and its independent input validator. It does not invoke the executable Tool
// or authorization policy. Blank arguments are normalized to the empty object.
func (c Contract) Prepare(call chat.ToolCall) (Invocation, error) {
	if c.state == nil {
		return Invocation{}, fmt.Errorf("%w: contract is zero", ErrInvalidInvocation)
	}
	if err := call.Validate(); err != nil {
		return Invocation{}, fmt.Errorf("%w: %w", ErrInvalidInvocation, err)
	}
	if call.Name != c.state.definition.Name {
		return Invocation{}, fmt.Errorf(
			"%w: call name %q does not match bound tool %q",
			ErrInvalidInvocation, call.Name, c.state.definition.Name,
		)
	}
	arguments := []byte(call.Arguments)
	if len(bytes.TrimSpace(arguments)) == 0 {
		arguments = []byte("{}")
	}
	if err := c.validateInput(arguments); err != nil {
		return Invocation{}, fmt.Errorf("%w: arguments: %w", ErrInvalidInvocation, err)
	}
	owned := append([]byte(nil), arguments...)
	return Invocation{contract: c.state, arguments: owned}, nil
}

func (c Contract) validateInput(arguments []byte) (err error) {
	if schemaErr := c.state.input.Validate(arguments); schemaErr != nil {
		return schemaErr
	}
	if c.state.validate == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("input validator panicked: %v", recovered)
		}
	}()
	return c.state.validate(arguments)
}

// Call executes an Invocation prepared by this Binding's exact Contract. It
// rejects another binding's contract even when the public Tool name matches.
func (b Binding) Call(ctx context.Context, invocation Invocation) (chat.ToolOutput, error) {
	if b.contract.state == nil || invocation.contract != b.contract.state {
		return chat.ToolOutput{}, fmt.Errorf("%w: invocation does not belong to binding", ErrInvalidInvocation)
	}
	return b.executable.Call(ctx, invocation)
}

// Arguments returns an owned copy of the validated JSON object.
func (i Invocation) Arguments() []byte { return append([]byte(nil), i.arguments...) }
