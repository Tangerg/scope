package chroma

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math/big"
	"reflect"
	"strconv"

	"github.com/Tangerg/scope/core/metadata"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
)

func documentMetadata(values map[string]any) (v2.DocumentMetadata, error) {
	normalized := make(map[string]any, len(values))
	for key, value := range values {
		normalized[key] = metadataNumberRepresentation(value)
	}
	result, err := v2.NewDocumentMetadataFromMap(normalized)
	if err != nil {
		return nil, err
	}
	// The SDK's scalar and array number encoders have different precision.
	// Verify its actual wire value rather than only its intermediate Go values.
	encoded, err := jsonv2.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("chroma: encode metadata: %w", err)
	}
	var restored metadata.Map
	if err = jsonv2.Unmarshal(encoded, &restored); err != nil {
		return nil, fmt.Errorf("chroma: decode encoded metadata: %w", err)
	}
	restoredValues, err := restored.Values()
	if err != nil {
		return nil, err
	}
	for key, value := range values {
		if actual, present := restoredValues[key]; !present || !metadataValueEqual(value, actual) {
			return nil, fmt.Errorf("chroma: metadata %q cannot be represented without loss", key)
		}
	}
	return result, nil
}

// Chroma selects integer encoding by spelling, although 1.0 and 1e0 are integers.
func metadataNumberRepresentation(value any) any {
	switch value := value.(type) {
	case json.Number:
		exact, ok := new(big.Rat).SetString(value.String())
		if ok && exact.IsInt() && exact.Num().IsInt64() {
			return json.Number(strconv.FormatInt(exact.Num().Int64(), 10))
		}
	case []any:
		items := make([]any, len(value))
		for index, item := range value {
			items[index] = metadataNumberRepresentation(item)
		}
		return items
	}
	return value
}

func metadataValueEqual(expected, actual any) bool {
	switch expected := expected.(type) {
	case json.Number:
		number, ok := actual.(json.Number)
		if !ok {
			return false
		}
		exact, valid := new(big.Rat).SetString(expected.String())
		restored, representable := new(big.Rat).SetString(number.String())
		return valid && representable && exact.Cmp(restored) == 0
	case []any:
		items, ok := actual.([]any)
		if !ok || len(items) != len(expected) {
			return false
		}
		for index, item := range expected {
			if !metadataValueEqual(item, items[index]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(expected, actual)
	}
}
