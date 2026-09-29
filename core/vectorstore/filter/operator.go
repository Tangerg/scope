package filter

import "fmt"

type Operator string

// Operators form a closed vocabulary. Provider translations must agree with Match.
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

	// OpLike matches the whole string, case-sensitively: % matches any run and
	// _ matches exactly one character.
	OpLike Operator = "like"

	OpIs Operator = "is"
)

func (o Operator) String() string { return string(o) }

var operatorNames = map[Operator]string{
	OpEqual:        "EQ",
	OpNotEqual:     "NE",
	OpLess:         "LT",
	OpLessEqual:    "LE",
	OpGreater:      "GT",
	OpGreaterEqual: "GE",
	OpAnd:          "AND",
	OpOr:           "OR",
	OpNot:          "NOT",
	OpIn:           "IN",
	OpHas:          "HAS",
	OpLike:         "LIKE",
	OpIs:           "IS",
}

func (o Operator) Name() string {
	if name, ok := operatorNames[o]; ok {
		return name
	}
	return "INVALID"
}

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

// inverseComparison applies only to non-null operands.
func (o Operator) inverseComparison() (Operator, error) {
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
