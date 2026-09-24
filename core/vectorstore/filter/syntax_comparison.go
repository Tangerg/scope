package filter

func compare[L IdentifierValue | *IndexExpr, R LiteralValue](left L, right R, operator Operator) *BinaryExpr {
	return &BinaryExpr{
		left:     leftOperand(left),
		operator: operator,
		right:    NewLiteral(right),
	}
}

// EQ builds `left == right` for any literal type. Examples:
// `id == 1`, `name == 'john'`, `arr[0] == 'value'`.
func EQ[L IdentifierValue | *IndexExpr, R LiteralValue](left L, right R) *BinaryExpr {
	return compare(left, right, OpEqual)
}

func NE[L IdentifierValue | *IndexExpr, R LiteralValue](left L, right R) *BinaryExpr {
	return compare(left, right, OpNotEqual)
}

// LT builds strict less-than. The right operand must be numeric.
func LT[L IdentifierValue | *IndexExpr, R Number | *Literal](left L, right R) *BinaryExpr {
	return compare(left, right, OpLess)
}

// LE builds less-than-or-equal. The right operand must be
// numeric.
func LE[L IdentifierValue | *IndexExpr, R Number | *Literal](left L, right R) *BinaryExpr {
	return compare(left, right, OpLessEqual)
}

// GT builds strict greater-than. The right operand must be
// numeric.
func GT[L IdentifierValue | *IndexExpr, R Number | *Literal](left L, right R) *BinaryExpr {
	return compare(left, right, OpGreater)
}

// GE builds greater-than-or-equal. The right operand must be
// numeric.
func GE[L IdentifierValue | *IndexExpr, R Number | *Literal](left L, right R) *BinaryExpr {
	return compare(left, right, OpGreaterEqual)
}

// In builds a membership predicate. The right operand is converted via
// [NewListLiteral]. Examples: `status IN ('active','pending')`,
// `id IN (1,2,3)`.
func In[L IdentifierValue | *IndexExpr, R ListValue](left L, right R) *BinaryExpr {
	return &BinaryExpr{
		left:     leftOperand(left),
		operator: OpIn,
		right:    NewListLiteral(right),
	}
}

// Has requires the collection selected by left to contain right as a
// complete element. It is the inverse direction of [In], which tests one
// selected scalar against a caller-supplied list.
func Has[L IdentifierValue | *IndexExpr, R LiteralValue](left L, right R) *BinaryExpr {
	return &BinaryExpr{
		left:     leftOperand(left),
		operator: OpHas,
		right:    NewLiteral(right),
	}
}

// Like builds pattern matching. The right operand must be a string. Examples:
// `name LIKE 'John%'`, `email LIKE '%@gmail.com'`.
func Like[L IdentifierValue | *IndexExpr, R string | *Literal](left L, right R) *BinaryExpr {
	return &BinaryExpr{
		left:     leftOperand(left),
		operator: OpLike,
		right:    NewLiteral(right),
	}
}

func IsNull[L IdentifierValue | *IndexExpr](left L) *BinaryExpr {
	return &BinaryExpr{left: leftOperand(left), operator: OpIs, right: &Literal{kind: LiteralNull, text: string(LiteralNull)}}
}

func IsNotNull[L IdentifierValue | *IndexExpr](left L) *UnaryExpr {
	return Not(IsNull(left))
}
