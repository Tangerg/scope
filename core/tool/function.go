package tool

import (
	"context"
	"fmt"
)

// Func composes a Signature with a function of the call's arguments. A Tool
// whose effect depends on the call itself, such as one deduplicating on its
// IdempotencyKey or resuming a Continuation, implements Tool around a
// Signature instead, because those facts belong to the Invocation.
type Func[In, Out any] struct {
	config    FuncConfig
	signature Signature[In, Out]
	function  func(context.Context, In) (Out, error)
}

type FuncConfig struct {
	Name        string
	Description string
	Effect      Effect
}

func NewFunc[In, Out any](config FuncConfig, function func(context.Context, In) (Out, error)) (Func[In, Out], error) {
	if function == nil {
		return Func[In, Out]{}, fmt.Errorf("%w: function is nil", ErrInvalidTool)
	}
	signature, err := NewSignature[In, Out]()
	if err != nil {
		return Func[In, Out]{}, fmt.Errorf("%w: signature: %w", ErrInvalidTool, err)
	}
	functionTool := Func[In, Out]{config: config, signature: signature, function: function}
	if err := functionTool.Definition().Validate(); err != nil {
		return Func[In, Out]{}, fmt.Errorf("%w: definition: %w", ErrInvalidTool, err)
	}
	return functionTool, nil
}

func (f Func[In, Out]) Definition() Definition {
	if f.function == nil {
		return Definition{}
	}
	return Definition{Name: f.config.Name, Description: f.config.Description, InputSchema: f.signature.InputSchema(), OutputSchema: f.signature.OutputSchema(), Effect: f.config.Effect}
}

func (f Func[In, Out]) Call(ctx context.Context, invocation Invocation) (Result, error) {
	if f.function == nil {
		return Result{}, fmt.Errorf("%w: function tool is nil", ErrInvalidTool)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	input, err := f.signature.Decode(invocation)
	if err != nil {
		return Result{}, fmt.Errorf("tool: decode function arguments: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	output, err := f.function(ctx, input)
	if err != nil {
		return Result{}, err
	}
	result, err := f.signature.Encode(output)
	if err != nil {
		return Result{}, fmt.Errorf("tool: encode function result: %w", err)
	}
	return Result{Output: result}, nil
}

func (f Func[In, Out]) InputValidator() func([]byte) error { return f.signature.InputValidator() }
