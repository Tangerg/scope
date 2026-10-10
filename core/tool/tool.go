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

// Tool executes invocations admitted by its exact frozen Contract. Errors carry
// no retry, pause, or abort policy; the driving runtime owns those decisions.
type Tool interface {
	// Definition returns a detached, valid schema snapshot. Callers may expose or
	// mutate the returned value without changing subsequent calls or execution.
	Definition() chat.ToolDefinition
	// Call executes one schema-validated invocation. Implementations still own
	// capability-specific semantic validation. Ordinary failure is returned as
	// error without assigning retry or control-flow meaning. Implementations must
	// honor ctx and must not retain the invocation or its arguments.
	// On error, the returned output is not consumed; use [Failure] to preserve
	// complete failure content, including acknowledged partial effects. Use
	// [CallError] for non-final execution evidence without asserting an outcome.
	Call(ctx context.Context, invocation Invocation) (chat.ToolOutput, error)
}

// contractState is the frozen trust boundary. The compiled input Schema owns the
// input-schema fact: it already carries the canonical JSON the Definition
// projects, so storing a separate chat.ToolDefinition would keep a second,
// possibly key-reordered, copy of the same schema. Only the name and description,
// which the Schema does not hold, are kept alongside it.
type contractState struct {
	name        string
	description string
	input       corejsonschema.Schema
	validate    func([]byte) error
}

// Contract is the immutable trust boundary from an untrusted chat.ToolCall to
// an Invocation. It retains the frozen definition, compiled schema, and input
// validator without retaining an executable Tool. It is safe for concurrent use
// independently of the Tool.
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
		contract:   Contract{state: &contractState{name: definition.Name, description: definition.Description, input: input, validate: validator}},
		executable: executable,
	}, nil
}

// Contract returns the exact frozen contract used to prepare this Binding's
// invocations. Retaining it does not retain the executable Tool.
func (b Binding) Contract() Contract { return b.contract }

// Definition projects an independent snapshot of the frozen definition from its
// single schema owner. InputSchema is the compiled Schema's canonical JSON, so
// the exposed definition and the enforced contract never disagree on bytes.
func (c Contract) Definition() chat.ToolDefinition {
	if c.state == nil {
		return chat.ToolDefinition{}
	}
	return chat.ToolDefinition{
		Name:        c.state.name,
		Description: c.state.description,
		InputSchema: c.state.input.JSON(),
	}
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
	if call.Name != c.state.name {
		return Invocation{}, fmt.Errorf(
			"%w: call name %q does not match bound tool %q",
			ErrInvalidInvocation, call.Name, c.state.name,
		)
	}
	arguments := []byte(call.Arguments)
	if len(bytes.TrimSpace(arguments)) == 0 {
		arguments = []byte("{}")
	}
	if err := c.validateInput(arguments); err != nil {
		return Invocation{}, fmt.Errorf("%w: arguments: %w", ErrInvalidInvocation, err)
	}
	return Invocation{contract: c.state, arguments: arguments}, nil
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
			err = &InputValidationPanicError{Value: recovered}
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
func (i Invocation) Arguments() []byte { return bytes.Clone(i.arguments) }
