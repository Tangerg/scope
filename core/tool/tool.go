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
// receives only an Invocation promoted by the exact frozen [Binding].
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

type boundContract struct {
	executable Tool
	definition chat.ToolDefinition
	input      corejsonschema.Schema
}

// Binding freezes one Tool definition and compiles its input schema. It is the
// canonical trust boundary between an untrusted [chat.ToolCall] and execution.
// A successfully constructed Binding and every Invocation it creates are safe
// for concurrent use when the underlying Tool is safe for concurrent calls.
type Binding struct {
	contract *boundContract
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
	return Binding{contract: &boundContract{
		executable: executable,
		definition: definition.Clone(),
		input:      input,
	}}, nil
}

// Definition returns an independent snapshot of the frozen definition.
func (b Binding) Definition() chat.ToolDefinition {
	if b.contract == nil {
		return chat.ToolDefinition{}
	}
	return b.contract.definition.Clone()
}

// Invocation is a complete JSON object validated against one exact frozen Tool
// definition. Its fields are intentionally private: only Binding.Prepare can
// promote an untrusted model proposal into an executable invocation.
type Invocation struct {
	contract  *boundContract
	arguments []byte
}

// Prepare validates identity, RFC 7493 JSON syntax, and the frozen input schema
// without invoking the Tool or any optional Tool capability. Blank arguments
// are normalized to the empty object.
func (b Binding) Prepare(call chat.ToolCall) (Invocation, error) {
	if b.contract == nil {
		return Invocation{}, fmt.Errorf("%w: binding is zero", ErrInvalidInvocation)
	}
	if err := call.Validate(); err != nil {
		return Invocation{}, fmt.Errorf("%w: %w", ErrInvalidInvocation, err)
	}
	if call.Name != b.contract.definition.Name {
		return Invocation{}, fmt.Errorf(
			"%w: call name %q does not match bound tool %q",
			ErrInvalidInvocation, call.Name, b.contract.definition.Name,
		)
	}
	arguments := []byte(call.Arguments)
	if len(bytes.TrimSpace(arguments)) == 0 {
		arguments = []byte("{}")
	}
	if err := b.contract.input.Validate(arguments); err != nil {
		return Invocation{}, fmt.Errorf("%w: arguments: %w", ErrInvalidInvocation, err)
	}
	owned := append([]byte(nil), arguments...)
	return Invocation{contract: b.contract, arguments: owned}, nil
}

// Call executes an Invocation created by this exact Binding. It rejects values
// promoted by another binding even when the public Tool name happens to match.
func (b Binding) Call(ctx context.Context, invocation Invocation) (chat.ToolOutput, error) {
	if b.contract == nil || invocation.contract == nil || b.contract != invocation.contract {
		return chat.ToolOutput{}, fmt.Errorf("%w: invocation does not belong to binding", ErrInvalidInvocation)
	}
	return b.contract.executable.Call(ctx, invocation)
}

// Arguments returns an owned copy of the validated JSON object.
func (i Invocation) Arguments() []byte { return append([]byte(nil), i.arguments...) }
