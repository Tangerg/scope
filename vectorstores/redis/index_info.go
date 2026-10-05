package redis

import (
	"fmt"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

type indexInfo struct {
	name       string
	keyType    string
	prefixes   []string
	filtered   bool
	attributes []goredis.FTAttribute
}

// FTInfoResult omits the index FILTER expression. Read the one raw response
// rather than accepting a partially decoded definition as namespace evidence.
func parseIndexInfo(raw any) (indexInfo, error) {
	var info indexInfo
	fields, err := responseFields(raw)
	if err != nil {
		return info, err
	}
	info.name, _ = fields["index_name"].(string)
	definition, err := responseFields(fields["index_definition"])
	if err != nil {
		return info, err
	}
	info.keyType, _ = definition["key_type"].(string)
	prefixes, validPrefixes := definition["prefixes"].([]any)
	if !validPrefixes {
		return info, fmt.Errorf("redis: FT.INFO prefixes have type %T", definition["prefixes"])
	}
	for _, value := range prefixes {
		prefix, ok := value.(string)
		if !ok {
			return info, fmt.Errorf("redis: FT.INFO prefix has type %T", value)
		}
		info.prefixes = append(info.prefixes, prefix)
	}
	if value, present := definition["filter"]; present {
		expression, ok := value.(string)
		if !ok {
			return info, fmt.Errorf("redis: FT.INFO filter has type %T", value)
		}
		info.filtered = expression != ""
	}

	if options, ok := fields["index_options"].([]any); ok {
		for _, option := range options {
			if strings.HasPrefix(strings.ToUpper(fmt.Sprint(option)), "FILTER") {
				info.filtered = true
			}
		}
	} else {
		return info, fmt.Errorf("redis: FT.INFO index_options have type %T", fields["index_options"])
	}
	attributes, ok := fields["attributes"].([]any)
	if !ok {
		return info, fmt.Errorf("redis: FT.INFO attributes have type %T", fields["attributes"])
	}
	for _, rawAttribute := range attributes {
		attribute, err := responseFields(rawAttribute)
		if err != nil {
			return info, err
		}
		name, _ := attribute["attribute"].(string)
		identifier, _ := attribute["identifier"].(string)
		kind, _ := attribute["type"].(string)
		if kind != "VECTOR" {
			info.attributes = append(info.attributes, goredis.FTAttribute{Identifier: identifier, Attribute: name, Type: kind})
			continue
		}
		dataType, _ := attribute["data_type"].(string)
		metric, _ := attribute["distance_metric"].(string)
		dim, err := strconv.Atoi(fmt.Sprint(attribute["dim"]))
		if err != nil {
			return info, fmt.Errorf("redis: invalid FT.INFO vector dimension: %w", err)
		}
		info.attributes = append(info.attributes, goredis.FTAttribute{Identifier: identifier, Attribute: name, Type: kind, DataType: dataType, DistanceMetric: metric, Dim: dim})
	}
	return info, nil
}

func responseFields(raw any) (map[string]any, error) {
	fields := make(map[string]any)
	switch raw := raw.(type) {
	case map[string]any:
		return raw, nil
	case map[any]any:
		for key, value := range raw {
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("redis: native response field name has type %T", key)
			}
			fields[name] = value
		}
	default:
		return nil, fmt.Errorf("redis: requires a RESP3 map, received %T", raw)
	}
	return fields, nil
}
