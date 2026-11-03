package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"

	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
)

var (
	ErrInvalidTool       = errors.New("tool: invalid tool")
	ErrInvalidInvocation = errors.New("tool: invalid invocation")
)

// Tool executes invocations admitted by its exact frozen Contract. Errors carry
// no retry, pause, or abort policy; the driving runtime owns those decisions.
type Tool interface {
	Caller
	// Definition returns a detached, valid schema snapshot. Callers may expose or
	// mutate the returned value without changing subsequent calls or execution.
	Definition() Definition
}

// Compiled schemas own the canonical JSON projected by Definition. Keeping a
// separate mutable definition would let admission and advertised schemas drift.
type contractState struct {
	name        string
	description string
	input       corejsonschema.Schema
	output      corejsonschema.Schema
	effect      Effect
	validate    func([]byte) error
}

// Contract is the immutable trust boundary from an untrusted Request to
// an Invocation. It retains the frozen definition, compiled schema, and input
// validator without retaining an executable Tool. It is safe for concurrent use
// independently of the Tool.
type Contract struct {
	state *contractState
}

// Binding associates one immutable Contract with the Tool that executes it.
// Calls may run concurrently when the underlying Tool supports concurrent use.
type Binding struct {
	contract    Contract
	executable  Caller
	middlewares []CallMiddleware
}

// Bind freezes and validates executable. Definition is read exactly once.
func Bind(executable Tool, middlewares ...CallMiddleware) (Binding, error) {
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
	var output corejsonschema.Schema
	if len(definition.OutputSchema) != 0 {
		output, err = corejsonschema.Parse(definition.OutputSchema)
		if err != nil {
			return Binding{}, fmt.Errorf("%w: output schema: %w", ErrInvalidTool, err)
		}
	}
	for index, middleware := range middlewares {
		if middleware == nil {
			return Binding{}, fmt.Errorf("%w: middleware[%d] is nil", ErrInvalidTool, index)
		}
	}
	validator, err := inputValidator(executable)
	if err != nil {
		return Binding{}, fmt.Errorf("%w: input validation: %w", ErrInvalidTool, err)
	}
	return Binding{
		contract:    Contract{state: &contractState{name: definition.Name, description: definition.Description, input: input, output: output, effect: definition.Effect, validate: validator}},
		executable:  executable,
		middlewares: slices.Clone(middlewares),
	}, nil
}

// Contract returns the exact frozen contract used to prepare this Binding's
// invocations. Retaining it does not retain the executable Tool.
func (b Binding) Contract() Contract { return b.contract }

func (c Contract) Effect() Effect {
	if c.state == nil {
		return EffectUnsafe
	}
	return c.state.effect
}

// Name identifies the frozen execution target without copying its schema.
func (c Contract) Name() string {
	if c.state == nil {
		return ""
	}
	return c.state.name
}

// Definition projects an independent snapshot of the frozen definition from its
// single schema owner. InputSchema is the compiled Schema's canonical JSON, so
// the exposed definition and the enforced contract never disagree on bytes.
func (c Contract) Definition() Definition {
	if c.state == nil {
		return Definition{}
	}
	return Definition{
		Name:         c.state.name,
		Description:  c.state.description,
		InputSchema:  c.state.input.JSON(),
		OutputSchema: c.state.output.JSON(),
		Effect:       c.state.effect,
	}
}

// Invocation is a complete JSON object admitted by one exact frozen Tool
// contract. Its fields are intentionally private: only Contract.Prepare can
// promote an untrusted model proposal into an executable invocation.
type Invocation struct {
	contract       *contractState
	provenance     *invocationIdentity
	arguments      []byte
	idempotencyKey IdempotencyKey
	continuation   *Continuation
}

// Prepare validates identity, RFC 7493 JSON syntax, the frozen input schema,
// and its independent input validator. It does not invoke the executable Tool
// or authorization policy. Blank arguments are normalized to the empty object.
// A driving runtime may supply a Continuation only for the same call that
// produced its committed InputRequest.
func (c Contract) Prepare(call Request, admission Admission) (Invocation, error) {
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
	if err := admission.validate(); err != nil {
		return Invocation{}, err
	}
	var continuation *Continuation
	if admission.Continuation != nil {
		continuation = new(*admission.Continuation)
	}
	return Invocation{contract: c.state, provenance: new(invocationIdentity), arguments: arguments, idempotencyKey: admission.IdempotencyKey, continuation: continuation}, nil
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
func (b Binding) Call(ctx context.Context, invocation Invocation) (Result, error) {
	if b.contract.state == nil || invocation.contract != b.contract.state {
		return Result{}, fmt.Errorf("%w: invocation does not belong to binding", ErrInvalidInvocation)
	}
	result, err := b.call(ctx, invocation, 0)
	if err != nil {
		return Result{}, err
	}
	if err := result.Validate(); err != nil {
		return Result{}, fmt.Errorf("tool: result: %w", err)
	}
	if result.InputRequest == nil && b.contract.state.output.Valid() {
		if err := b.contract.state.output.Validate(result.Output.Details); err != nil {
			return Result{}, fmt.Errorf("%w: structured output: %w", ErrInvalidResult, err)
		}
	}
	return result, nil
}

// Arguments returns an owned copy of the validated JSON object.
func (i Invocation) Arguments() []byte { return bytes.Clone(i.arguments) }

func (i Invocation) IdempotencyKey() IdempotencyKey { return i.idempotencyKey }

func (i Invocation) Continuation() (Continuation, bool) {
	if i.continuation == nil {
		return Continuation{}, false
	}
	return *i.continuation, true
}

func (b Binding) call(ctx context.Context, invocation Invocation, index int) (Result, error) {
	if index == len(b.middlewares) {
		return b.executable.Call(ctx, invocation)
	}
	forward := &callContinuation{invocation: invocation, next: CallerFunc(func(ctx context.Context, invocation Invocation) (Result, error) {
		return b.call(ctx, invocation, index+1)
	})}
	defer forward.close()
	next := b.middlewares[index](forward)
	if lo.IsNil(next) {
		return Result{}, fmt.Errorf("%w: middleware[%d] returned nil", ErrInvalidTool, index)
	}
	result, err := next.Call(ctx, invocation)
	if validationErr := forward.validate(result); validationErr != nil {
		return Result{}, validationErr
	}
	return result, err
}

func (i Invocation) Definition() Definition { return (Contract{state: i.contract}).Definition() }

type invocationIdentity byte
