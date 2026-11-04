package tool

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"reflect"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/jsonschema"
)

// Signature owns the typed codec and both of its schema projections. Custom
// decoders must be deterministic, bounded and free of external effects because
// admission uses the same decoder before execution. Admission of a typed input
// is that strict decode followed by the input's own Validate method when it
// declares one, so a request type states its domain rules once. Output bypasses the JSON
// codec to preserve media. Strings are text-only outputs and declare no Details
// schema; other values encode into Details under the reflected output schema.
// The declared output type selects the codec once, so interface values cannot
// change the advertised output contract at runtime.
type Signature[In, Out any] struct {
	input       json.RawMessage
	output      json.RawMessage
	outputCodec signatureOutputCodec
}

type signatureOutputCodec uint8

const (
	signatureJSONOutput signatureOutputCodec = iota
	signatureTextOutput
	signatureDirectOutput
)

func NewSignature[In, Out any]() (Signature[In, Out], error) {
	inputType := reflect.TypeFor[In]()
	if inputType.Kind() == reflect.Pointer {
		inputType = inputType.Elem()
	}
	if inputType.Kind() != reflect.Struct {
		return Signature[In, Out]{}, fmt.Errorf("%w: function input type %s must be a struct or pointer to struct", ErrInvalidTool, inputType)
	}
	input, err := jsonschema.For[In]()
	if err != nil {
		return Signature[In, Out]{}, err
	}
	signature := Signature[In, Out]{input: input.JSON(), outputCodec: signatureJSONOutput}
	switch outputType := reflect.TypeFor[Out](); {
	case outputType == reflect.TypeFor[Output]():
		signature.outputCodec = signatureDirectOutput
	case outputType.Kind() == reflect.String:
		signature.outputCodec = signatureTextOutput
	default:
		output, err := jsonschema.For[Out]()
		if err != nil {
			return Signature[In, Out]{}, err
		}
		signature.output = output.JSON()
	}
	return signature, nil
}

func (s Signature[In, Out]) InputSchema() json.RawMessage  { return bytes.Clone(s.input) }
func (s Signature[In, Out]) OutputSchema() json.RawMessage { return bytes.Clone(s.output) }

func (s Signature[In, Out]) Decode(invocation Invocation) (In, error) {
	if len(s.input) == 0 {
		var zero In
		return zero, fmt.Errorf("%w: signature is zero", ErrInvalidTool)
	}
	return admitInput[In](invocation.arguments)
}

func (Signature[In, Out]) InputValidator() func([]byte) error {
	return func(arguments []byte) error {
		_, err := admitInput[In](arguments)
		return err
	}
}

// validatingInput is the domain admission a typed input may declare beyond its
// schema. Either receiver kind is honored, so a type need not choose one for it.
type validatingInput interface{ Validate() error }

func admitInput[In any](arguments []byte) (In, error) {
	var input In
	if err := jsonv2.Unmarshal(arguments, &input, jsonv2.RejectUnknownMembers(true)); err != nil {
		return input, err
	}
	validator, declared := any(&input).(validatingInput)
	if !declared {
		validator, declared = any(input).(validatingInput)
	}
	if !declared {
		return input, nil
	}
	if lo.IsNil(validator) {
		return input, fmt.Errorf("%w: input is null", ErrInvalidRequest)
	}
	if err := validator.Validate(); err != nil {
		return input, err
	}
	return input, nil
}

func (s Signature[In, Out]) Encode(output Out) (Output, error) {
	if len(s.input) == 0 {
		return Output{}, fmt.Errorf("%w: signature is zero", ErrInvalidTool)
	}
	switch s.outputCodec {
	case signatureDirectOutput:
		result := any(output).(Output)
		if err := result.Validate(); err != nil {
			return Output{}, err
		}
		return result.Clone(), nil
	case signatureTextOutput:
		result := NewTextOutput(reflect.ValueOf(output).String())
		if err := result.Validate(); err != nil {
			return Output{}, err
		}
		return result, nil
	}
	encoded, err := jsonv2.Marshal(output)
	if err != nil {
		return Output{}, err
	}
	result, err := NewJSONOutput(encoded)
	if err != nil {
		return Output{}, err
	}

	return result, nil
}
