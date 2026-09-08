package filter

import "fmt"

// Operator is a semantic filter operation. It is independent from lexer token
// kinds and is safe for provider adapters to switch on.
type Operator string

// Operators are a closed set because every backend compiles the same AST into
// its own dialect. Adding one here is a deliberate act that obliges each
// compiler to answer for it; leaving the set open would let a filter mean
// different things on different stores.
//
// Closing the set is only half of that. A compiler also has to know what each
// operator means, and where a provider's nearest equivalent is not an exact
// match the compiler owes the caller the documented behavior rather than the
// convenient one. [Match] is the reference: it evaluates this AST directly, so
// what it does is what an operator means, and a translation that disagrees
// makes one filter behave differently depending on which store answers it.
const (
	OpEqual        Operator = "=="
	OpNotEqual     Operator = "!="
	OpLess         Operator = "<"
	OpLessEqual    Operator = "<="
	OpGreater      Operator = ">"
	OpGreaterEqual Operator = ">="
	OpAnd          Operator = "and"
	OpOr           Operator = "or"
	OpNot          Operator = "not"
	OpIn           Operator = "in"
	OpHas          Operator = "has"

	// OpLike is SQL LIKE over a string: % matches any run of characters, _
	// matches exactly one, the pattern must match the whole value rather than
	// a substring of it, and comparison is case-sensitive. A provider whose
	// pattern match is case-insensitive by default — Postgres ILIKE, a MongoDB
	// regex with the "i" option, a MySQL collation ending in _ci — is a wider
	// match than this operator asks for, not a synonym for it.
	OpLike Operator = "like"

	OpIs Operator = "is"
)

func (o Operator) String() string { return string(o) }
func (o Operator) Name() string {
	switch o {
	case OpEqual:
		return "EQ"
	case OpNotEqual:
		return "NE"
	case OpLess:
		return "LT"
	case OpLessEqual:
		return "LE"
	case OpGreater:
		return "GT"
	case OpGreaterEqual:
		return "GE"
	case OpAnd:
		return "AND"
	case OpOr:
		return "OR"
	case OpNot:
		return "NOT"
	case OpIn:
		return "IN"
	case OpHas:
		return "HAS"
	case OpLike:
		return "LIKE"
	case OpIs:
		return "IS"
	default:
		return "INVALID"
	}
}
func (o Operator) Is(other Operator) bool   { return o == other }
func (o Operator) IsEqualityOperator() bool { return o == OpEqual || o == OpNotEqual }
func (o Operator) IsOrderingOperator() bool {
	return o == OpLess || o == OpLessEqual || o == OpGreater || o == OpGreaterEqual
}
func (o Operator) IsComparisonOperator() bool {
	return o.IsEqualityOperator() || o.IsOrderingOperator()
}
func (o Operator) IsLogicalOperator() bool { return o == OpAnd || o == OpOr }
func (o Operator) IsMembershipOperator() bool {
	return o == OpIn || o == OpHas
}
func (o Operator) IsMatchingOperator() bool { return o.IsMembershipOperator() || o == OpLike }
func (o Operator) IsNullOperator() bool     { return o == OpIs }
func (o Operator) IsBinaryOperator() bool {
	return o.IsComparisonOperator() || o.IsLogicalOperator() || o.IsMatchingOperator() || o.IsNullOperator()
}
func (o Operator) IsUnaryOperator() bool { return o == OpNot }

// LogicalString returns the canonical uppercase form of a logical operator.
func (o Operator) LogicalString() (string, error) {
	if !o.IsLogicalOperator() {
		return "", fmt.Errorf("filter: format logical operator: expected logical operator, got %s", o.Name())
	}
	return o.Name(), nil
}

// Inverse returns the exact inverse comparison operator.
func (o Operator) Inverse() (Operator, error) {
	switch o {
	case OpEqual:
		return OpNotEqual, nil
	case OpNotEqual:
		return OpEqual, nil
	case OpLess:
		return OpGreaterEqual, nil
	case OpLessEqual:
		return OpGreater, nil
	case OpGreater:
		return OpLessEqual, nil
	case OpGreaterEqual:
		return OpLess, nil
	default:
		return "", fmt.Errorf("filter: invert operator: %s has no direct inverse", o.Name())
	}
}

func (o Operator) dual() Operator {
	if o == OpAnd {
		return OpOr
	}
	if o == OpOr {
		return OpAnd
	}
	return ""
}
