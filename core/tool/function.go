package tool

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"reflect"

	"github.com/Tangerg/scope/core/chat"
	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
)

// Func adapts a typed Go function to [Tool]. It is immutable and as safe for
// concurrent calls as the wrapped function.
//
// A Func validates arguments by strictly decoding them into In, not against a
// retained compiled schema, so it owns only the canonical input-schema JSON it
// projects into its Definition. [Bind] compiles that JSON when it needs
// schema-level admission for the Tool contract.
type Func[In, Out any] struct {
	config      FuncConfig
	inputSchema json.RawMessage
	function    func(context.Context, In) (Out, error)
}

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
	schema := input.JSON()
	definition := chat.ToolDefinition{
		Name:        config.Name,
		Description: config.Description,
		InputSchema: schema,
	}
	if err := definition.Validate(); err != nil {
		return zero, fmt.Errorf("%w: definition: %w", ErrInvalidTool, err)
	}
	return Func[In, Out]{
		config:      config,
		inputSchema: schema,
		function:    function,
	}, nil
}

func validateFuncInputType(input reflect.Type) error {
	if input.Kind() == reflect.Pointer {
		input = input.Elem()
	}
	if input.Kind() != reflect.Struct {
		return fmt.Errorf("function input type %s must be a struct or pointer to struct", input)
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
		InputSchema: bytes.Clone(f.inputSchema),
	}
}

// Call checks cancellation before entering the application function. Once
// dispatched, that function owns the outcome, including acknowledged effects;
// cancellation never replaces its completed result. Successful output must
// satisfy the canonical ToolOutput contract.
func (f Func[In, Out]) Call(ctx context.Context, invocation Invocation) (chat.ToolOutput, error) {
	if f.function == nil {
		return chat.ToolOutput{}, fmt.Errorf("%w: function tool is nil", ErrInvalidTool)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return chat.ToolOutput{}, ctxErr
	}
	input, err := decodeFuncInput[In](invocation.Arguments())
	if err != nil {
		return chat.ToolOutput{}, fmt.Errorf("tool: decode function arguments: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return chat.ToolOutput{}, ctxErr
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
		result := chat.NewTextToolOutput(value.String())
		if err := result.Validate(); err != nil {
			return chat.ToolOutput{}, err
		}
		return result, nil
	}
	encoded, err := jsonv2.Marshal(output)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	return chat.NewJSONToolOutput(encoded)
}
