package mongodb

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
)

// BSON numbers must retain their decimal value after retrieval as JSON.
func metadataNumber(number json.Number) (any, error) {
	exact, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return nil, fmt.Errorf("mongodb: invalid metadata number %q", number)
	}
	if exact.IsInt() && exact.Num().IsInt64() {
		return exact.Num().Int64(), nil
	}
	value, err := number.Float64()
	if err != nil {
		return nil, fmt.Errorf("mongodb: metadata number %q: %w", number, err)
	}
	restored, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	if !ok || exact.Cmp(restored) != 0 {
		return nil, fmt.Errorf("mongodb: metadata number %q cannot be represented without loss", number)
	}
	return value, nil
}

func metadataDocument(values map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(values))
	for key, value := range values {
		converted, err := metadataValue(value)
		if err != nil {
			return nil, fmt.Errorf("mongodb: metadata %q: %w", key, err)
		}
		result[key] = converted
	}
	return result, nil
}

func metadataValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		return metadataNumber(value)
	case map[string]any:
		return metadataDocument(value)
	case []any:
		items := make([]any, len(value))
		for index, item := range value {
			converted, err := metadataValue(item)
			if err != nil {
				return nil, fmt.Errorf("mongodb: metadata item %d: %w", index, err)
			}
			items[index] = converted
		}
		return items, nil
	default:
		return value, nil
	}
}
