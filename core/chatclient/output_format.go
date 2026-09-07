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
	// ErrInvalidOutputFormat identifies a format that cannot define one request
	// contract and terminal decoder.
	ErrInvalidOutputFormat = errors.New("chatclient: invalid output format")

	// ErrInvalidOutput identifies a response that cannot be decoded under the
	// requested contract.
	ErrInvalidOutput = errors.New("chatclient: invalid output")
)

// OutputFormat couples a provider-neutral request contract with the decoder
// for its complete response. Pass one to [Client.Output].
type OutputFormat[T any] struct {
	contract chat.OutputFormat
	decoder  func([]byte) (T, error)
}

// Text selects provider-native text output without post-processing.
func Text() OutputFormat[string] {
	return OutputFormat[string]{
		contract: chat.OutputFormat{Type: chat.OutputFormatText},
		decoder:  func(raw []byte) (string, error) { return string(raw), nil },
	}
}

// JSON selects provider-native JSON output and rejects unknown object members
// when decoding T.
func JSON[T any]() OutputFormat[T] {
	return OutputFormat[T]{
		contract: chat.OutputFormat{Type: chat.OutputFormatJSON},
		decoder:  func(raw []byte) (T, error) { return decodeJSON[T](raw, nil) },
	}
}

// JSONSchemaConfig supplies the stable identity of a schema derived from T.
type JSONSchemaConfig struct {
	Name        string
	Description string
}

// JSONSchema returns a result format coupled to the named contract derived
// from T.
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
		contract: contract,
		decoder:  func(raw []byte) (T, error) { return decodeJSON[T](raw, &schema) },
	}, nil
}

func (o OutputFormat[T]) validate() error {
	if err := o.contract.Validate(); err != nil {
		return fmt.Errorf("%w: contract: %w", ErrInvalidOutputFormat, err)
	}
	if o.decoder == nil {
		return fmt.Errorf("%w: nil decoder", ErrInvalidOutputFormat)
	}
	return nil
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
	value, err := o.decoder([]byte(text))
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%w: decode: %w", ErrInvalidOutput, err)
	}
	return value, nil
}

func decodeJSON[T any](raw []byte, schema *corejsonschema.Schema) (T, error) {
	var decoded T
	if schema != nil {
		if err := schema.Validate(raw); err != nil {
			return decoded, err
		}
	}
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return decoded, err
	}
	return decoded, nil
}
