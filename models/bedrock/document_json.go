package bedrock

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	smithydocument "github.com/aws/smithy-go/document"
)

// Smithy's encoder requires its own arbitrary-precision number type. Parsing
// directly into that native value tree avoids both float rounding and strings.
func documentFromJSON(raw []byte) (document.Interface, error) {
	numbers := jsonv2.UnmarshalFromFunc(func(decoder *jsontext.Decoder, value *any) error {
		if decoder.PeekKind() != jsontext.KindNumber {
			return errors.ErrUnsupported
		}
		raw, err := decoder.ReadValue()
		if err != nil {
			return err
		}
		*value = smithydocument.Number(raw)
		return nil
	})
	var value any
	if err := jsonv2.Unmarshal(raw, &value, jsonv2.WithUnmarshalers(numbers)); err != nil {
		return nil, err
	}
	return document.NewLazyDocument(value), nil
}
