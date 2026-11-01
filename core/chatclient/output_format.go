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

// Format couples a provider-neutral request contract with the decoder
// for its complete response. Pass one to [Client.Output], or call its Decode on
// a response obtained elsewhere so every caller shares one decode rule.
type Format[T any] struct {
	formatType        chat.OutputFormatType
	schemaName        string
	schemaDescription string
	schema            corejsonschema.Schema
	decoder           func([]byte) (T, error)
}

// Text selects provider-native text output without post-processing.
func Text() Format[string] {
	return Format[string]{
		formatType: chat.OutputFormatText,
		decoder:    func(raw []byte) (string, error) { return string(raw), nil },
	}
}

// JSON selects provider-native JSON output and rejects unknown object members
// when decoding T.
func JSON[T any]() Format[T] {
	return Format[T]{
		formatType: chat.OutputFormatJSON,
		decoder:    decodeJSON[T],
	}
}

type JSONSchemaConfig struct {
	Name        string
	Description string
}

func JSONSchema[T any](config JSONSchemaConfig) (Format[T], error) {
	schema, err := corejsonschema.For[T]()
	if err != nil {
		return Format[T]{}, fmt.Errorf("%w: derive schema: %w", ErrInvalidOutputFormat, err)
	}
	contract, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{
		Name:        config.Name,
		Description: config.Description,
		Schema:      schema.JSON(),
	})
	if err != nil {
		return Format[T]{}, fmt.Errorf("%w: %w", ErrInvalidOutputFormat, err)
	}
	return Format[T]{
		formatType:        contract.Type,
		schemaName:        contract.Name,
		schemaDescription: contract.Description,
		schema:            schema,
		decoder:           decodeJSON[T],
	}, nil
}

// The compiled schema owns both request bytes and response admission; a
// retained wire copy could drift from the decoder's contract.
func (f Format[T]) Contract() chat.OutputFormat {
	return chat.OutputFormat{
		Type: f.formatType, Name: f.schemaName, Description: f.schemaDescription, Schema: f.schema.JSON(),
	}
}

// validate guards against a zero-value format that skipped the constructors.
// The constructors freeze a valid contract at construction (Text and JSON are
// fixed valid literals; JSONSchema derives, compiles, and validates its schema),
// and the fields are private, so there is no other way to hold a contract.
// Re-validating it here would recompile the JSON Schema on every call and
// discard the result.
func (f Format[T]) validate() error {
	if f.decoder == nil {
		return fmt.Errorf("%w: nil decoder", ErrInvalidOutputFormat)
	}
	return nil
}

// Decode decodes an already-obtained response into T under the same rule as
// [Client.Output], without a model call, so a caller that already holds a
// response, such as a managed Agent, reuses one structured-output rule instead
// of a weaker copy.
func (f Format[T]) Decode(response *chat.Response) (T, error) {
	var zero T
	if err := f.validate(); err != nil {
		return zero, err
	}
	return f.decodeResponse(response, nil)
}

func (f Format[T]) decodeResponse(response *chat.Response, responseErr error) (T, error) {
	var zero T
	if responseErr != nil {
		return zero, responseErr
	}
	if err := response.Validate(); err != nil {
		return zero, fmt.Errorf("%w: response: %w", ErrInvalidOutput, err)
	}
	text, err := f.completedText(response.Output)
	if err != nil {
		return zero, err
	}
	return f.decodeText(text)
}

func (f Format[T]) completedText(output *chat.Output) (string, error) {
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

func (f Format[T]) decodeText(text string) (T, error) {
	raw := []byte(text)
	if f.schema.Valid() {
		if err := f.schema.Validate(raw); err != nil {
			var zero T
			return zero, fmt.Errorf("%w: decode: %w", ErrInvalidOutput, err)
		}
	}
	value, err := f.decoder(raw)
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
