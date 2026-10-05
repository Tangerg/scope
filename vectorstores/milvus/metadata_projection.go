package milvus

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type metadataKind string

const (
	metadataNumber     metadataKind = "number"
	metadataString     metadataKind = "string"
	metadataBool       metadataKind = "boolean"
	metadataArray      metadataKind = "array"
	metadataRuneDigits              = 6
)

type metadataProjection struct {
	Present []string                  `json:"present"`
	Kinds   map[metadataKind][]string `json:"kinds"`
	Scalars map[string]string         `json:"scalars"`
	Members map[string][]string       `json:"members"`
}

func projectMetadata(attributes metadata.Map) ([]byte, []byte, error) {
	if attributes == nil {
		attributes = metadata.Map{}
	}
	values, err := attributes.Values()
	if err != nil {
		return nil, nil, err
	}
	projection := metadataProjection{
		Kinds:   map[metadataKind][]string{metadataNumber: {}, metadataString: {}, metadataBool: {}, metadataArray: {}},
		Scalars: make(map[string]string), Members: make(map[string][]string),
	}
	for key, value := range values {
		if err = projection.collect(value, []any{key}); err != nil {
			return nil, nil, err
		}
	}
	slices.Sort(projection.Present)
	for _, paths := range projection.Kinds {
		slices.Sort(paths)
	}
	encoded, err := jsonv2.Marshal(attributes)
	if err != nil {
		return nil, nil, err
	}
	derived, err := jsonv2.Marshal(projection)
	if err != nil {
		return nil, nil, err
	}
	return encoded, derived, nil
}

func (m *metadataProjection) collect(value any, path []any) error {
	if value == nil {
		return nil
	}
	encoded, err := encodeMetadataPath(path)
	if err != nil {
		return err
	}
	m.Present = append(m.Present, encoded)
	scalar, kind, ok, err := encodeMetadataScalar(value)
	if err != nil {
		return err
	}
	if ok {
		m.Kinds[kind] = append(m.Kinds[kind], encoded)
		m.Scalars[encoded] = scalar
		return nil
	}
	switch nested := value.(type) {
	case map[string]any:
		for key, child := range nested {
			if err = m.collect(child, append(slices.Clone(path), key)); err != nil {
				return err
			}
		}
	case []any:
		m.Kinds[metadataArray] = append(m.Kinds[metadataArray], encoded)
		members := make([]string, 0, len(nested))
		for index, child := range nested {
			member, _, isScalar, memberErr := encodeMetadataScalar(child)
			if memberErr != nil {
				return memberErr
			}
			if isScalar {
				members = append(members, member)
			}
			if err = m.collect(child, append(slices.Clone(path), uint64(index))); err != nil {
				return err
			}
		}
		m.Members[encoded] = members
	default:
		return fmt.Errorf("milvus: unsupported metadata value %T", value)
	}
	return nil
}

func encodeMetadataPath(path []any) (string, error) {
	encoded, err := jsonv2.Marshal(path)
	if err != nil {
		return "", fmt.Errorf("milvus: encode metadata path: %w", err)
	}
	return string(encoded), nil
}

func projectedPath(expression *filter.BinaryExpr) (string, error) {
	path, err := expression.Path()
	if err != nil {
		return "", err
	}
	segments := make([]any, 0, len(path))
	for _, segment := range path {
		if index, ok := segment.Index(); ok {
			segments = append(segments, index)
		} else {
			key, _ := segment.Key()
			segments = append(segments, key)
		}
	}
	return encodeMetadataPath(segments)
}

func encodeMetadataScalar(value any) (string, metadataKind, bool, error) {
	switch scalar := value.(type) {
	case json.Number:
		encoded, err := encodeMetadataNumber(scalar.String())
		return string(metadataNumber) + ":" + encoded, metadataNumber, true, err
	case string:
		return string(metadataString) + ":" + encodeMetadataString(scalar, false), metadataString, true, nil
	case bool:
		return string(metadataBool) + ":" + strconv.FormatBool(scalar), metadataBool, true, nil
	case nil, map[string]any, []any:
		return "", "", false, nil
	default:
		return "", "", false, fmt.Errorf("milvus: unsupported metadata scalar %T", value)
	}
}

// Fixed-width ASCII rune tokens keep native LIKE's byte wildcard and escape
// rules from changing Core's character matching, including Unicode and newlines.
func encodeMetadataString(value string, pattern bool) string {
	var encoded strings.Builder
	for _, character := range value {
		if pattern && character == '%' {
			encoded.WriteByte('%')
		} else if pattern && character == '_' {
			encoded.WriteString("[" + strings.Repeat("_", metadataRuneDigits) + "]")
		} else {
			fmt.Fprintf(&encoded, "[%0*x]", metadataRuneDigits, character)
		}
	}
	return encoded.String()
}

// Native JSON numbers can be compared through double. A decimal's sign,
// adjusted exponent, and significant digits instead preserve its exact order
// using native string comparison. Negative keys reverse that ordering.
func encodeMetadataNumber(value string) (string, error) {
	// Core compares json.Number through this decoder. A projection must not
	// create numeric matches for values Core cannot represent.
	if _, ok := new(big.Rat).SetString(value); !ok {
		return "", fmt.Errorf("milvus: metadata number %q is outside Core's numeric range", value)
	}
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	mantissa, exponentText, exponentPresent := strings.Cut(value, "e")
	if !exponentPresent {
		mantissa, exponentText, exponentPresent = strings.Cut(value, "E")
	}
	exponent := new(big.Int)
	if exponentPresent {
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return "", fmt.Errorf("milvus: invalid metadata number exponent %q", exponentText)
		}
	}
	integer, fraction, _ := strings.Cut(mantissa, ".")
	digits := integer + fraction
	trimmed := strings.TrimLeft(digits, "0")
	if trimmed == "" {
		return "1", nil
	}
	exponent.Add(exponent, big.NewInt(int64(len(integer)-(len(digits)-len(trimmed))-1)))
	if !exponent.IsInt64() {
		return "", errors.New("milvus: metadata number exponent is outside int64")
	}
	digits = strings.TrimRight(trimmed, "0")
	order := uint64(exponent.Int64()) ^ (uint64(1) << 63)
	if !negative {
		return fmt.Sprintf("2%020d%s/", order, digits), nil
	}
	var reversed strings.Builder
	for index := range digits {
		reversed.WriteByte('9' - (digits[index] - '0'))
	}
	return fmt.Sprintf("0%020d%s:", ^order, reversed.String()), nil
}
