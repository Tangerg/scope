package pinecone

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"google.golang.org/protobuf/types/known/structpb"
)

func payloadValues(values map[string]any) (*structpb.Struct, error) {
	fields := make(map[string]*structpb.Value, len(values))
	for key, value := range values {
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
	case map[string]any:
		fields, err := payloadValues(value)
		if err != nil {
			return nil, err
		}
		return structpb.NewStructValue(fields), nil
	case []any:
		items := make([]*structpb.Value, len(value))
		for index, item := range value {
			converted, err := payloadValue(item)
			if err != nil {
				return nil, fmt.Errorf("pinecone: metadata item %d: %w", index, err)
			}
			items[index] = converted
		}
		return structpb.NewListValue(&structpb.ListValue{Values: items}), nil
	default:
		return structpb.NewValue(value)
	}
}
