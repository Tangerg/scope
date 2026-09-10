package neo4j

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
)

// propertyNumber maps JSON numbers to the backend's integer or double domain.
// A double must preserve the decimal value when encoded back to JSON.
func propertyNumber(number json.Number) (any, error) {
	exact, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return nil, fmt.Errorf("neo4j: invalid metadata number %q", number)
	}
	if exact.IsInt() && exact.Num().IsInt64() {
		return exact.Num().Int64(), nil
	}
	value, err := number.Float64()
	if err != nil {
		return nil, fmt.Errorf("neo4j: metadata number %q: %w", number, err)
	}
	restored, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	if !ok || exact.Cmp(restored) != 0 {
		return nil, fmt.Errorf("neo4j: metadata number %q cannot be represented without loss", number)
	}
	return value, nil
}

func propertyValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		return propertyNumber(value)
	case []any:
		values := make([]any, len(value))
		for index, item := range value {
			converted, err := propertyValue(item)
			if err != nil {
				return nil, fmt.Errorf("neo4j: metadata item %d: %w", index, err)
			}
			values[index] = converted
		}
		return values, nil
	case map[string]any:
		return nil, errors.New("neo4j: node properties cannot contain a metadata object")
	default:
		return value, nil
	}
}
