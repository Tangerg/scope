package tool

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"

	"github.com/Tangerg/scope/core/chat"
	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
)

// Func adapts a typed Go function to [Tool]. It owns the derived input
// contract, strict argument decoding, invocation, and result encoding.
//
// Func is immutable after construction and is safe for concurrent calls when
// the wrapped function is safe for concurrent calls.
type Func[In, Out any] struct {
	config   FuncConfig
	input    corejsonschema.Schema
	function func(context.Context, In) (Out, error)
}

// FuncConfig describes a typed function tool. NewFunc derives the model-visible
// schema and the independent input validator from In.
type FuncConfig struct {
	Name        string
	Description string
}

// NewFunc derives the model-visible JSON Schema from the Go input type. Its
// independent input validator uses the same strict decoder as Call, so Bind
// admits arguments before any function executes, even when JSON Schema cannot
// express all decoder constraints. Custom JSON and text decoders in In must be
// deterministic, bounded, side-effect-free, and safe for concurrent use.
func NewFunc[In, Out any](config FuncConfig, function func(context.Context, In) (Out, error)) (Func[In, Out], error) {
	var zero Func[In, Out]
	if function == nil {
		return zero, fmt.Errorf("%w: function is nil", ErrInvalidTool)
	}
	inputType := reflect.TypeFor[In]()
	if err := validateFuncInputType(inputType); err != nil {
		return zero, fmt.Errorf("%w: %w", ErrInvalidTool, err)
	}
	input, err := corejsonschema.For[In]()
	if err != nil {
		return zero, fmt.Errorf("%w: %w", ErrInvalidTool, err)
	}
	definition := chat.ToolDefinition{
		Name:        config.Name,
		Description: config.Description,
		InputSchema: input.JSON(),
	}
	if err := definition.Validate(); err != nil {
		return zero, fmt.Errorf("%w: definition: %w", ErrInvalidTool, err)
	}
	return Func[In, Out]{
		config:   config,
		input:    input,
		function: function,
	}, nil
}

func validateFuncInputType(input reflect.Type) error {
	if input == nil {
		return errors.New("tool: function input type is nil")
	}
	if input.Kind() == reflect.Pointer {
		input = input.Elem()
	}
	if input.Kind() != reflect.Struct {
		return fmt.Errorf("tool: function input type %s must be a struct or pointer to struct", input)
	}
	return nil
}

func (f Func[In, Out]) Definition() chat.ToolDefinition {
	if f.function == nil {
		return chat.ToolDefinition{}
	}
	return chat.ToolDefinition{
		Name:        f.config.Name,
		Description: f.config.Description,
		InputSchema: f.input.JSON(),
	}
}

// Call strictly decodes a promoted invocation, invokes the wrapped function,
// and returns a provider-neutral result.
func (f Func[In, Out]) Call(ctx context.Context, invocation Invocation) (chat.ToolOutput, error) {
	if f.function == nil {
		return chat.ToolOutput{}, fmt.Errorf("%w: function tool is nil", ErrInvalidTool)
	}
	input, err := decodeFuncInput[In](invocation.Arguments())
	if err != nil {
		return chat.ToolOutput{}, fmt.Errorf("tool: decode function arguments: %w", err)
	}
	output, err := f.function(ctx, input)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	result, err := f.encodeResult(output)
	if err != nil {
		return chat.ToolOutput{}, fmt.Errorf("tool: encode function result: %w", err)
	}
	return result, nil
}

// InputValidator retains only the input type's decoding behavior. It captures
// neither this Func nor its application function.
func (Func[In, Out]) InputValidator() func([]byte) error {
	return func(arguments []byte) error {
		_, err := decodeFuncInput[In](arguments)
		return err
	}
}

func decodeFuncInput[In any](arguments []byte) (In, error) {
	var input In
	if err := jsonv2.Unmarshal(arguments, &input, jsonv2.RejectUnknownMembers(true)); err != nil {
		return input, err
	}
	return input, nil
}

func (Func[In, Out]) encodeResult(output Out) (chat.ToolOutput, error) {
	value := reflect.ValueOf(output)
	if value.IsValid() && value.Kind() == reflect.String {
		return chat.NewTextToolOutput(value.String()), nil
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	return chat.NewJSONToolOutput(encoded)
}
