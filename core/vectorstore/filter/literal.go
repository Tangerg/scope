package filter

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

type LiteralKind string

const (
	LiteralString LiteralKind = "string"
	LiteralNumber LiteralKind = "number"
	LiteralBool   LiteralKind = "bool"
	LiteralNull   LiteralKind = "null"
)

const firstInvalidSignedIndex = 1 << 63

// Literal is an immutable scalar constant. Text is the canonical semantic
// representation; typed methods reject mismatched kinds.
type Literal struct {
	kind  LiteralKind
	text  string
	start Position
	end   Position
}

func (*Literal) expr() {}

func (l *Literal) Kind() LiteralKind {
	if l == nil {
		return ""
	}
	return l.kind
}

func (l *Literal) Text() string {
	if l == nil {
		return ""
	}
	return l.text
}

func (l *Literal) Start() Position {
	if l == nil {
		return Position{}
	}
	return l.start
}

func (l *Literal) End() Position {
	if l == nil {
		return Position{}
	}
	return l.end
}

func (l *Literal) Equal(other Expr) bool {
	o, ok := other.(*Literal)
	return ok && l != nil && o != nil && l.kind == o.kind && l.text == o.text
}

func (l *Literal) IsString() bool { return l != nil && l.kind == LiteralString }
func (l *Literal) IsNumber() bool { return l != nil && l.kind == LiteralNumber }
func (l *Literal) IsBool() bool   { return l != nil && l.kind == LiteralBool }
func (l *Literal) IsNull() bool   { return l != nil && l.kind == LiteralNull }

func (l *Literal) IsSameKind(other *Literal) bool {
	return l != nil && other != nil && l.kind == other.kind
}

func (l *Literal) AsString() (string, error) {
	if l == nil {
		return "", errors.New("filter: read string literal: literal is nil")
	}
	if !l.IsString() {
		return "", fmt.Errorf("filter: read string literal: expected string, got %s", l.kind)
	}
	if !utf8.ValidString(l.text) {
		return "", errors.New("filter: read string literal: invalid UTF-8")
	}
	return l.text, nil
}

func (l *Literal) AsNumber() (json.Number, error) {
	if l == nil {
		return "", errors.New("filter: read number literal: literal is nil")
	}
	if !l.IsNumber() {
		return "", fmt.Errorf("filter: read number literal: expected number, got %s", l.kind)
	}
	var number json.Number
	if err := jsonv2.Unmarshal([]byte(l.text), &number); err != nil {
		return "", fmt.Errorf("filter: read number literal: parse %q: %w", l.text, err)
	}
	return number, nil
}

func (l *Literal) AsBool() (bool, error) {
	if l == nil {
		return false, errors.New("filter: read boolean literal: literal is nil")
	}
	if !l.IsBool() {
		return false, fmt.Errorf("filter: read boolean literal: expected bool, got %s", l.kind)
	}
	b, err := strconv.ParseBool(l.text)
	if err != nil {
		return false, fmt.Errorf("filter: read boolean literal: parse %q: %w", l.text, err)
	}
	return b, nil
}

// NumberText renders a plain decimal numeral for provider grammars that do not
// accept exponent notation. Integral literals keep their exact digits; decimal
// and exponent literals preserve the float64 selected during construction.
// Text returns the canonical form, which may use an exponent.
func (l *Literal) NumberText() (string, error) {
	if _, err := l.numberRat(); err != nil {
		return "", err
	}
	if !strings.ContainsAny(l.text, ".eE") {
		return l.text, nil
	}
	number, err := strconv.ParseFloat(l.text, 64)
	if err != nil {
		return "", fmt.Errorf("filter: render number literal: invalid number %q: %w", l.text, err)
	}
	return strconv.FormatFloat(number, 'f', -1, 64), nil
}

func (l *Literal) IsInteger() (bool, error) {
	number, err := l.numberRat()
	if err != nil {
		return false, err
	}
	return number.IsInt(), nil
}

// Int64 converts an integral number literal without rounding.
func (l *Literal) Int64() (int64, error) {
	number, err := l.numberRat()
	if err != nil {
		return 0, err
	}
	if !number.IsInt() {
		return 0, fmt.Errorf("filter: convert literal to int64: number %q is not an integer", l.text)
	}
	integer := number.Num()
	if !integer.IsInt64() {
		return 0, fmt.Errorf("filter: convert literal to int64: integer %q exceeds int64", l.text)
	}
	return integer.Int64(), nil
}

// Int converts an integral number literal without rounding and rejects values
// outside the platform int range.
func (l *Literal) Int() (int, error) {
	value, err := l.Int64()
	if err != nil {
		return 0, err
	}
	converted := int(value)
	if int64(converted) != value {
		return 0, fmt.Errorf("filter: convert literal to int: integer %q exceeds int", l.text)
	}
	return converted, nil
}

// Float64 converts a number for provider APIs that accept a double. Integral
// values are rejected when conversion would change their exact value.
func (l *Literal) Float64() (float64, error) {
	number, err := l.numberRat()
	if err != nil {
		return 0, err
	}
	numberValue, err := l.AsNumber()
	if err != nil {
		return 0, err
	}
	value, err := numberValue.Float64()
	if err != nil {
		return 0, fmt.Errorf("filter: convert literal to float64: number %q is not a float64: %w", l.text, err)
	}
	if number.IsInt() && new(big.Rat).SetFloat64(value).Cmp(number) != 0 {
		return 0, fmt.Errorf("filter: convert literal to float64: integer %q loses precision", l.text)
	}
	return value, nil
}

// Float32 converts a number for provider APIs that accept a float. It rejects
// overflow and integral values that would be rounded.
func (l *Literal) Float32() (float32, error) {
	number, err := l.numberRat()
	if err != nil {
		return 0, err
	}
	numberValue, err := l.AsNumber()
	if err != nil {
		return 0, err
	}
	value, err := numberValue.Float64()
	if err != nil {
		return 0, fmt.Errorf("filter: convert literal to float32: number %q is not a float64: %w", l.text, err)
	}
	converted := float32(value)
	if math.IsInf(float64(converted), 0) {
		return 0, fmt.Errorf("filter: convert literal to float32: number %q exceeds float32", l.text)
	}
	if number.IsInt() && new(big.Rat).SetFloat64(float64(converted)).Cmp(number) != 0 {
		return 0, fmt.Errorf("filter: convert literal to float32: integer %q loses precision", l.text)
	}
	return converted, nil
}

// Key converts a string or non-negative integral number literal into a
// metadata index key.
func (l *Literal) Key() (string, error) {
	switch {
	case l.IsString():
		return l.AsString()
	case l.IsNumber():
		value, err := l.Value()
		if err != nil {
			return "", err
		}
		switch number := value.(type) {
		case int64:
			if number < 0 {
				return "", errors.New("filter: convert literal to index key: numeric index must be non-negative")
			}
			return strconv.FormatInt(number, 10), nil
		case uint64:
			if number > math.MaxInt64 {
				return "", errors.New("filter: convert literal to index key: numeric index exceeds int64")
			}
			return strconv.FormatUint(number, 10), nil
		case float64:
			if number < 0 || number >= firstInvalidSignedIndex || math.Trunc(number) != number {
				return "", errors.New("filter: convert literal to index key: numeric index must be a non-negative integer")
			}
			return strconv.FormatUint(uint64(number), 10), nil
		default:
			return "", fmt.Errorf("filter: convert literal to index key: unsupported numeric index type %T", value)
		}
	default:
		return "", errors.New("filter: convert literal to index key: index must be a string or number literal")
	}
}

// Value decodes the literal into a Go scalar. Decimal and exponent literals
// use the float64 representation selected during literal construction.
func (l *Literal) Value() (any, error) {
	if l == nil {
		return nil, errors.New("filter: decode literal value: literal is nil")
	}
	switch {
	case l.IsString():
		return l.AsString()
	case l.IsNumber():
		return l.decodeNumber()
	case l.IsBool():
		return l.AsBool()
	default:
		return nil, fmt.Errorf("filter: decode literal value: unsupported kind %s", l.kind)
	}
}

func (l *Literal) decodeNumber() (any, error) {
	if strings.ContainsAny(l.text, ".eE") {
		number, err := strconv.ParseFloat(l.text, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("filter: decode literal value: invalid number %q", l.text)
		}
		return number, nil
	}
	if strings.HasPrefix(l.text, "-") {
		number, err := strconv.ParseInt(l.text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("filter: decode literal value: invalid integer %q: %w", l.text, err)
		}
		return number, nil
	}
	number, err := strconv.ParseUint(l.text, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("filter: decode literal value: invalid integer %q: %w", l.text, err)
	}
	if number <= math.MaxInt64 {
		return int64(number), nil
	}
	return number, nil
}

func (l *Literal) numberRat() (*big.Rat, error) {
	if l == nil || !l.IsNumber() {
		return nil, fmt.Errorf("filter: expected number literal, got %v", l)
	}
	number, ok := new(big.Rat).SetString(l.text)
	if !ok {
		return nil, fmt.Errorf("filter: invalid number literal %q", l.text)
	}
	return number, nil
}

func (l *Literal) isIntegerIndex() bool {
	if !l.IsNumber() {
		return false
	}
	number, ok := new(big.Rat).SetString(l.text)
	return ok && number.Sign() >= 0 && number.IsInt() && number.Num().IsInt64()
}

func (l *Literal) validate() error {
	if l == nil {
		return errors.New("filter: literal is nil")
	}

	switch l.kind {
	case LiteralString:
		_, err := l.AsString()
		return err
	case LiteralNull:
		if l.text != string(LiteralNull) {
			return fmt.Errorf("filter: invalid NULL literal %q at %s", l.text, l.Start())
		}
		return nil
	case LiteralNumber:
		canonical, err := canonicalNumber(l.text)
		if err != nil || canonical != l.text {
			return fmt.Errorf("filter: invalid number literal %q at %s", l.text, l.Start())
		}
		return nil
	case LiteralBool:
		if l.text != "true" && l.text != "false" {
			return fmt.Errorf("filter: invalid boolean literal %q at %s", l.text, l.Start())
		}
		return nil
	default:
		return fmt.Errorf("filter: invalid literal kind %q at %s", l.kind, l.Start())
	}
}
