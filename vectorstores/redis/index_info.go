package redis

import (
	"fmt"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

const (
	nativeHashKeyType        = "HASH"
	vectorElementTypeFloat32 = "FLOAT32"
)

type indexInfo struct {
	name       string
	keyType    string
	prefixes   []string
	filtered   bool
	attributes []goredis.FTAttribute
}

// FT.INFO response vocabulary. RESP3 reports a TAG separator under either
// spelling depending on the server version.
const (
	infoIndexName       = "index_name"
	infoIndexDefinition = "index_definition"
	infoKeyType         = "key_type"
	infoPrefixes        = "prefixes"
	infoFilter          = "filter"
	infoIndexOptions    = "index_options"
	infoFilterOption    = "FILTER"
	infoAttributes      = "attributes"
	infoAttribute       = "attribute"
	infoIdentifier      = "identifier"
	infoType            = "type"
	infoSeparator       = "SEPARATOR"
	infoSeparatorLower  = "separator"
	infoFlags           = "flags"
	infoNoIndexFlag     = "NOINDEX"
	infoDataType        = "data_type"
	infoDistanceMetric  = "distance_metric"
	infoDimension       = "dim"
	termsSeparator      = ","
)

// FTInfoResult omits the index FILTER expression. Read the one raw response
// rather than accepting a partially decoded definition as namespace evidence.
func parseIndexInfo(raw any) (indexInfo, error) {
	var info indexInfo
	fields, err := responseFields(raw)
	if err != nil {
		return info, err
	}
	info.name, _ = fields[infoIndexName].(string)
	if err = info.readDefinition(fields[infoIndexDefinition]); err != nil {
		return info, err
	}
	optionFiltered, err := filteredByOptions(fields[infoIndexOptions])
	if err != nil {
		return info, err
	}
	info.filtered = info.filtered || optionFiltered
	attributes, ok := fields[infoAttributes].([]any)
	if !ok {
		return info, fmt.Errorf("redis: FT.INFO attributes have type %T", fields[infoAttributes])
	}
	for _, rawAttribute := range attributes {
		attribute, err := parseIndexAttribute(rawAttribute)
		if err != nil {
			return info, err
		}
		info.attributes = append(info.attributes, attribute)
	}
	return info, nil
}

// readDefinition reads the key type, prefixes and filter of the definition.
func (i *indexInfo) readDefinition(raw any) error {
	definition, err := responseFields(raw)
	if err != nil {
		return err
	}
	i.keyType, _ = definition[infoKeyType].(string)
	prefixes, ok := definition[infoPrefixes].([]any)
	if !ok {
		return fmt.Errorf("redis: FT.INFO prefixes have type %T", definition[infoPrefixes])
	}
	for _, value := range prefixes {
		prefix, ok := value.(string)
		if !ok {
			return fmt.Errorf("redis: FT.INFO prefix has type %T", value)
		}
		i.prefixes = append(i.prefixes, prefix)
	}
	if value, present := definition[infoFilter]; present {
		expression, ok := value.(string)
		if !ok {
			return fmt.Errorf("redis: FT.INFO filter has type %T", value)
		}
		i.filtered = expression != ""
	}
	return nil
}

// filteredByOptions reports whether the index options declare a FILTER.
func filteredByOptions(raw any) (bool, error) {
	options, ok := raw.([]any)
	if !ok {
		return false, fmt.Errorf("redis: FT.INFO index_options have type %T", raw)
	}
	for _, option := range options {
		if strings.HasPrefix(strings.ToUpper(fmt.Sprint(option)), infoFilterOption) {
			return true, nil
		}
	}
	return false, nil
}

func parseIndexAttribute(raw any) (goredis.FTAttribute, error) {
	attribute, err := responseFields(raw)
	if err != nil {
		return goredis.FTAttribute{}, err
	}
	name, _ := attribute[infoAttribute].(string)
	identifier, _ := attribute[infoIdentifier].(string)
	kind, _ := attribute[infoType].(string)
	if name == termsField && kind == goredis.SearchFieldTypeTag.String() {
		if err = validateTermsAttribute(attribute); err != nil {
			return goredis.FTAttribute{}, err
		}
	}
	if kind != goredis.SearchFieldTypeVector.String() {
		return goredis.FTAttribute{Identifier: identifier, Attribute: name, Type: kind}, nil
	}
	dataType, _ := attribute[infoDataType].(string)
	metric, _ := attribute[infoDistanceMetric].(string)
	dim, err := strconv.Atoi(fmt.Sprint(attribute[infoDimension]))
	if err != nil {
		return goredis.FTAttribute{}, fmt.Errorf("redis: invalid FT.INFO vector dimension: %w", err)
	}
	return goredis.FTAttribute{Identifier: identifier, Attribute: name, Type: kind, DataType: dataType, DistanceMetric: metric, Dim: dim}, nil
}

// validateTermsAttribute requires the indexed comma TAG that keyword terms use.
func validateTermsAttribute(attribute map[string]any) error {
	separator, ok := attribute[infoSeparator].(string)
	if !ok {
		separator, ok = attribute[infoSeparatorLower].(string)
	}
	if !ok || separator != termsSeparator {
		return fmt.Errorf("redis: metadata_terms requires the comma TAG separator")
	}
	flags, _ := attribute[infoFlags].([]any)
	for _, flag := range flags {
		if strings.EqualFold(fmt.Sprint(flag), infoNoIndexFlag) {
			return fmt.Errorf("redis: metadata_terms must be indexed")
		}
	}
	return nil
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
