package qdrant

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/qdrant/go-client/qdrant"
)

// payloadNumber maps JSON numbers to the backend's integer or double domain.
// A double must preserve the decimal value when encoded back to JSON.
func payloadNumber(number json.Number) (any, error) {
	exact, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return nil, fmt.Errorf("qdrant: invalid metadata number %q", number)
	}
	if exact.IsInt() && exact.Num().IsInt64() {
		return exact.Num().Int64(), nil
	}
	value, err := number.Float64()
	if err != nil {
		return nil, fmt.Errorf("qdrant: metadata number %q: %w", number, err)
	}
	restored, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	if !ok || exact.Cmp(restored) != 0 {
		return nil, fmt.Errorf("qdrant: metadata number %q cannot be represented without loss", number)
	}
	return value, nil
}

func payloadValues(values map[string]any) (map[string]*qdrant.Value, error) {
	payload := make(map[string]*qdrant.Value, len(values))
	for key, value := range values {
		converted, err := payloadValue(value)
		if err != nil {
			return nil, fmt.Errorf("qdrant: metadata %q: %w", key, err)
		}
		payload[key] = converted
	}
	return payload, nil
}

func payloadValue(value any) (*qdrant.Value, error) {
	switch value := value.(type) {
	case json.Number:
		number, err := payloadNumber(value)
		if err != nil {
			return nil, err
		}
		return qdrant.NewValue(number)
	case map[string]any:
		fields, err := payloadValues(value)
		if err != nil {
			return nil, err
		}
		return qdrant.NewValueFromFields(fields), nil
	case []any:
		items := make([]*qdrant.Value, len(value))
		for index, item := range value {
			converted, err := payloadValue(item)
			if err != nil {
				return nil, fmt.Errorf("qdrant: metadata item %d: %w", index, err)
			}
			items[index] = converted
		}
		return qdrant.NewValueFromList(items...), nil
	default:
		return qdrant.NewValue(value)
	}
}
