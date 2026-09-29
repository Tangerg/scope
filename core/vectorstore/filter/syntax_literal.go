package filter

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

type Number interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

type LiteralValue interface {
	Number | string | bool | *Literal
}

func newLiteral(value any) (*Literal, error) {
	if value != nil {
		reflected := reflect.ValueOf(value)
		switch reflected.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return &Literal{kind: LiteralNumber, text: strconv.FormatInt(reflected.Int(), 10)}, nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return &Literal{kind: LiteralNumber, text: strconv.FormatUint(reflected.Uint(), 10)}, nil
		case reflect.Float32, reflect.Float64:
			number := reflected.Float()
			// Canonical zero has no sign; preserving -0 would fail Validate.
			if number == 0 {
				return &Literal{kind: LiteralNumber, text: "0"}, nil
			}
			return &Literal{
				kind: LiteralNumber,
				text: strconv.FormatFloat(number, 'g', -1, reflected.Type().Bits()),
			}, nil
		}
	}

	switch typed := value.(type) {
	case string:
		return &Literal{kind: LiteralString, text: typed}, nil

	case bool:
		return &Literal{kind: LiteralBool, text: strconv.FormatBool(typed)}, nil

	case *Literal:
		return typed, nil

	default:
		return nil, fmt.Errorf("filter: create literal: unsupported literal type %T (%v)",
			value, value)
	}
}

func NewLiteral[T LiteralValue](value T) *Literal {
	literal, err := newLiteral(value)
	if err != nil {
		panic(err)
	}
	return literal
}

func NewLiterals[T LiteralValue](values []T) []*Literal {
	literals := make([]*Literal, 0, len(values))
	for _, v := range values {
		literals = append(literals, NewLiteral(v))
	}
	return literals
}

func canonicalNumber(literal string) (string, error) {
	if !strings.ContainsAny(literal, ".eE") {
		if strings.HasPrefix(literal, "-") {
			number, err := strconv.ParseInt(literal, 10, 64)
			if err != nil {
				return "", fmt.Errorf("invalid integer %q", literal)
			}
			return strconv.FormatInt(number, 10), nil
		}
		number, err := strconv.ParseUint(literal, 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid integer %q", literal)
		}
		return strconv.FormatUint(number, 10), nil
	}

	number, err := strconv.ParseFloat(literal, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return "", fmt.Errorf("invalid number %q", literal)
	}
	if number == 0 {
		return "0", nil
	}
	return strconv.FormatFloat(number, 'g', -1, 64), nil
}

type ListValue interface {
	[]int | []int8 | []int16 | []int32 | []int64 |
		[]uint | []uint8 | []uint16 | []uint32 | []uint64 |
		[]float32 | []float64 | []string | []bool |
		[]*Literal | *ListLiteral
}

func newListLiteral(value any) (*ListLiteral, error) {
	switch typed := value.(type) {
	case *ListLiteral:
		return typed, nil
	case []*Literal:
		return &ListLiteral{values: slices.Clone(typed)}, nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice {
		return nil, fmt.Errorf("filter: create list literal: unsupported list type %T (%v)", value, value)
	}
	values := make([]*Literal, reflected.Len())
	for index := range values {
		literal, err := newLiteral(reflected.Index(index).Interface())
		if err != nil {
			return nil, err
		}
		values[index] = literal
	}
	return &ListLiteral{values: values}, nil
}

func NewListLiteral[T ListValue](value T) *ListLiteral {
	list, err := newListLiteral(value)
	if err != nil {
		panic(err)
	}
	return list
}
