package pinecone

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"
)

func payloadValues(values map[string]any) (*structpb.Struct, error) {
	fields := make(map[string]*structpb.Value, len(values))
	for key, value := range values {
		if strings.HasPrefix(key, "$") {
			return nil, fmt.Errorf("pinecone: %w: metadata key %q begins with $", errors.ErrUnsupported, key)
		}
		converted, err := payloadValue(value)
		if err != nil {
			return nil, fmt.Errorf("pinecone: metadata %q: %w", key, err)
		}
		fields[key] = converted
	}
	return &structpb.Struct{Fields: fields}, nil
}

func payloadValue(value any) (*structpb.Value, error) {
	switch value := value.(type) {
	case json.Number:
		number, err := value.Float64()
		if err != nil {
			return nil, fmt.Errorf("pinecone: metadata number %q: %w", value, err)
		}
		exact, valid := new(big.Rat).SetString(value.String())
		restored, representable := new(big.Rat).SetString(strconv.FormatFloat(number, 'g', -1, 64))
		if !valid || !representable || exact.Cmp(restored) != 0 {
			return nil, fmt.Errorf("pinecone: metadata number %q cannot be represented without loss", value)
		}
		return structpb.NewNumberValue(number), nil
	case []any:
		items := make([]*structpb.Value, len(value))
		for index, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("pinecone: %w: metadata arrays must contain strings", errors.ErrUnsupported)
			}
			items[index] = structpb.NewStringValue(text)
		}
		return structpb.NewListValue(&structpb.ListValue{Values: items}), nil
	case string, bool:
		return structpb.NewValue(value)

	default:
		return nil, fmt.Errorf("pinecone: %w: metadata value of type %T is not a string, number, boolean, or string list", errors.ErrUnsupported, value)
	}
}
