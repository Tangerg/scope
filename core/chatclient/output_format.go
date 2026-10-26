package chatclient

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/chat"
	corejsonschema "github.com/Tangerg/scope/core/jsonschema"
)

// OutputCompletionError preserves a provider outcome that cannot become the
// requested typed value. It unwraps to ErrInvalidOutput; callers may inspect
// FinishReason and Refusal without parsing an error message.
type OutputCompletionError struct {
	FinishReason chat.FinishReason
	Refusal      string
}

func (o *OutputCompletionError) Error() string {
	if o.Refusal != "" {
		return fmt.Sprintf("chatclient: output ended with %q: refusal: %s", o.FinishReason, o.Refusal)
	}
	return fmt.Sprintf("chatclient: output ended with %q", o.FinishReason)
}

func (*OutputCompletionError) Unwrap() error { return ErrInvalidOutput }

var (
	ErrInvalidOutputFormat = errors.New("chatclient: invalid output format")

	ErrInvalidOutput = errors.New("chatclient: invalid output")
)

// OutputFormat couples a provider-neutral request contract with the decoder
// for its complete response. Pass one to [Client.Output], or call its Decode on
// a response obtained elsewhere so every caller shares one decode rule.
type OutputFormat[T any] struct {
	formatType        chat.OutputFormatType
	schemaName        string
	schemaDescription string
	schema            corejsonschema.Schema
	decoder           func([]byte) (T, error)
}

// Text selects provider-native text output without post-processing.
func Text() OutputFormat[string] {
	return OutputFormat[string]{
		formatType: chat.OutputFormatText,
		decoder:    func(raw []byte) (string, error) { return string(raw), nil },
	}
}

// JSON selects provider-native JSON output and rejects unknown object members
// when decoding T.
func JSON[T any]() OutputFormat[T] {
	return OutputFormat[T]{
		formatType: chat.OutputFormatJSON,
		decoder:    decodeJSON[T],
	}
}

type JSONSchemaConfig struct {
	Name        string
	Description string
}

func JSONSchema[T any](config JSONSchemaConfig) (OutputFormat[T], error) {
	schema, err := corejsonschema.For[T]()
	if err != nil {
		return OutputFormat[T]{}, fmt.Errorf("%w: derive schema: %w", ErrInvalidOutputFormat, err)
	}
	contract, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{
		Name:        config.Name,
		Description: config.Description,
		Schema:      schema.JSON(),
	})
	if err != nil {
		return OutputFormat[T]{}, fmt.Errorf("%w: %w", ErrInvalidOutputFormat, err)
	}
	return OutputFormat[T]{
		formatType:        contract.Type,
		schemaName:        contract.Name,
		schemaDescription: contract.Description,
		schema:            schema,
		decoder:           decodeJSON[T],
	}, nil
}

// The compiled schema owns both request bytes and response admission; a
// retained wire copy could drift from the decoder's contract.
func (o OutputFormat[T]) contract() chat.OutputFormat {
	return chat.OutputFormat{
		Type: o.formatType, Name: o.schemaName, Description: o.schemaDescription, Schema: o.schema.JSON(),
	}
}

// validate guards against a zero-value format that skipped the constructors.
// The constructors freeze a valid contract at construction (Text and JSON are
// fixed valid literals; JSONSchema derives, compiles, and validates its schema),
// and the fields are private, so there is no other way to hold a contract.
// Re-validating it here would recompile the JSON Schema on every call and
// discard the result.
func (o OutputFormat[T]) validate() error {
	if o.decoder == nil {
		return fmt.Errorf("%w: nil decoder", ErrInvalidOutputFormat)
	}
	return nil
}

// Decode decodes an already-obtained response into T under the same rule as
// [Client.Output], without a model call, so a caller that already holds a
// response, such as a managed Agent, reuses one structured-output rule instead
// of a weaker copy.
func (o OutputFormat[T]) Decode(response *chat.Response) (T, error) {
	var zero T
	if err := o.validate(); err != nil {
		return zero, err
	}
	return o.decodeResponse(response, nil)
}

func (o OutputFormat[T]) decodeResponse(response *chat.Response, responseErr error) (T, error) {
	var zero T
	if responseErr != nil {
		return zero, responseErr
	}
	if response == nil {
		return zero, fmt.Errorf("%w: nil response", ErrInvalidOutput)
	}
	if err := response.Validate(); err != nil {
		return zero, fmt.Errorf("%w: response: %w", ErrInvalidOutput, err)
	}
	text, err := o.completedText(response.Output)
	if err != nil {
		return zero, err
	}
	return o.decodeText(text)
}

func (o OutputFormat[T]) completedText(output *chat.Output) (string, error) {
	var text, refusal strings.Builder
	var unsupported chat.PartKind
	if output.Message != nil {
		for _, part := range output.Message.Parts {
			switch part.Kind {
			case chat.PartText:
				text.WriteString(part.Text)
			case chat.PartRefusal:
				refusal.WriteString(part.Text)
			case chat.PartReasoning:
			default:
				unsupported = part.Kind
			}
		}
	}
	if output.FinishReason != chat.FinishReasonStop || refusal.Len() != 0 {
		return "", &OutputCompletionError{FinishReason: output.FinishReason, Refusal: refusal.String()}
	}
	if unsupported != "" {
		return "", fmt.Errorf("%w: typed output cannot contain %q parts", ErrInvalidOutput, unsupported)
	}
	if text.Len() == 0 {
		return "", fmt.Errorf("%w: completed output contains no text", ErrInvalidOutput)
	}
	return text.String(), nil
}

func (o OutputFormat[T]) decodeText(text string) (T, error) {
	raw := []byte(text)
	if o.schema.Valid() {
		if err := o.schema.Validate(raw); err != nil {
			var zero T
			return zero, fmt.Errorf("%w: decode: %w", ErrInvalidOutput, err)
		}
	}
	value, err := o.decoder(raw)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%w: decode: %w", ErrInvalidOutput, err)
	}
	return value, nil
}

func decodeJSON[T any](raw []byte) (T, error) {
	var decoded T
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return decoded, err
	}
	return decoded, nil
}
