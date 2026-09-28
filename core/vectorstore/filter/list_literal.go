package filter

import (
	"errors"
	"fmt"
	"slices"
)

// ListLiteral is an immutable homogeneous list used by IN expressions.
type ListLiteral struct {
	values []*Literal
	start  Position
	end    Position
}

func (*ListLiteral) expr() {}

func (l *ListLiteral) Len() int {
	if l == nil {
		return 0
	}
	return len(l.values)
}

func (l *ListLiteral) Literals() []*Literal {
	if l == nil {
		return nil
	}
	return slices.Clone(l.values)
}

func (l *ListLiteral) First() (*Literal, error) {
	if l == nil || len(l.values) == 0 {
		return nil, errors.New("filter: read first list literal: list is empty")
	}
	return l.values[0], nil
}

func (l *ListLiteral) Start() Position {
	if l == nil {
		return Position{}
	}
	return l.start
}

func (l *ListLiteral) End() Position {
	if l == nil {
		return Position{}
	}
	return l.end
}

func (l *ListLiteral) Equal(other Expr) bool {
	o, ok := other.(*ListLiteral)
	if !ok || l == nil || o == nil || len(l.values) != len(o.values) {
		return false
	}
	for i := range l.values {
		if !equalExpr(l.values[i], o.values[i]) {
			return false
		}
	}
	return true
}

func (l *ListLiteral) Values() (any, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	first := l.values[0]
	switch {
	case first.IsString():
		out := make([]string, 0, len(l.values))
		for _, literal := range l.values {
			value, err := literal.AsString()
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case first.IsNumber():
		return l.numberValues()
	case first.IsBool():
		out := make([]bool, 0, len(l.values))
		for _, literal := range l.values {
			value, err := literal.AsBool()
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("filter: decode list literal values: unsupported element kind %s", first.Kind())
	}
}

func (l *ListLiteral) numberValues() (any, error) {
	values, hasFloat, hasUint, err := l.decodeNumberValues()
	if err != nil {
		return nil, err
	}
	if hasFloat {
		return decimalListValues(values)
	}
	if hasUint {
		return unsignedListValues(values)
	}
	return signedListValues(values), nil
}

func (l *ListLiteral) decodeNumberValues() ([]any, bool, bool, error) {
	values := make([]any, 0, len(l.values))
	hasFloat := false
	hasUint := false
	for _, literal := range l.values {
		value, err := literal.Value()
		if err != nil {
			return nil, false, false, err
		}
		values = append(values, value)
		switch value.(type) {
		case float64:
			hasFloat = true
		case uint64:
			hasUint = true
		}
	}
	return values, hasFloat, hasUint, nil
}

func decimalListValues(values []any) ([]float64, error) {
	const maxExactFloatInteger = int64(1 << 53)
	out := make([]float64, 0, len(values))
	for _, value := range values {
		switch number := value.(type) {
		case int64:
			if number < -maxExactFloatInteger || number > maxExactFloatInteger {
				return nil, fmt.Errorf("filter: integer %d loses precision in a decimal list", number)
			}
			out = append(out, float64(number))
		case uint64:
			if number > uint64(maxExactFloatInteger) {
				return nil, fmt.Errorf("filter: integer %d loses precision in a decimal list", number)
			}
			out = append(out, float64(number))
		case float64:
			out = append(out, number)
		}
	}
	return out, nil
}

func unsignedListValues(values []any) ([]uint64, error) {
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		switch number := value.(type) {
		case int64:
			if number < 0 {
				return nil, errors.New("filter: numeric list spans signed and unsigned integer ranges")
			}
			out = append(out, uint64(number))
		case uint64:
			out = append(out, number)
		}
	}
	return out, nil
}

func signedListValues(values []any) []int64 {
	out := make([]int64, 0, len(values))
	for _, value := range values {
		out = append(out, value.(int64))
	}
	return out
}

func (l *ListLiteral) validate() error {
	if l == nil {
		return errors.New("filter: list literal is nil")
	}
	if len(l.values) == 0 {
		return fmt.Errorf("filter: list literal cannot be empty at %s", l.Start())
	}

	first := l.values[0]
	if first == nil {
		return fmt.Errorf("filter: list element 0 is nil at %s", l.Start())
	}
	if first.IsNull() {
		return fmt.Errorf("filter: list elements cannot be NULL at %s", first.Start())
	}
	if err := first.validate(); err != nil {
		return err
	}

	for index, value := range l.values[1:] {
		if value == nil {
			return fmt.Errorf("filter: list element %d is nil at %s", index+1, l.Start())
		}
		if value.kind != first.kind {
			return fmt.Errorf(
				"filter: list element %d has kind %s, expected %s at %s",
				index+1, value.kind, first.kind, value.Start(),
			)
		}
		if err := value.validate(); err != nil {
			return err
		}
	}
	return nil
}
