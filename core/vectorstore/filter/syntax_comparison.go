package filter

func compare[L IdentifierValue | *IndexExpr, R LiteralValue](left L, right R, operator Operator) *BinaryExpr {
	return &BinaryExpr{
		left:     leftOperand(left),
		operator: operator,
		right:    NewLiteral(right),
	}
}

func EQ[L IdentifierValue | *IndexExpr, R LiteralValue](left L, right R) *BinaryExpr {
	return compare(left, right, OpEqual)
}

func NE[L IdentifierValue | *IndexExpr, R LiteralValue](left L, right R) *BinaryExpr {
	return compare(left, right, OpNotEqual)
}

func LT[L IdentifierValue | *IndexExpr, R Number | *Literal](left L, right R) *BinaryExpr {
	return compare(left, right, OpLess)
}

func LE[L IdentifierValue | *IndexExpr, R Number | *Literal](left L, right R) *BinaryExpr {
	return compare(left, right, OpLessEqual)
}

func GT[L IdentifierValue | *IndexExpr, R Number | *Literal](left L, right R) *BinaryExpr {
	return compare(left, right, OpGreater)
}

func GE[L IdentifierValue | *IndexExpr, R Number | *Literal](left L, right R) *BinaryExpr {
	return compare(left, right, OpGreaterEqual)
}

func In[L IdentifierValue | *IndexExpr, R ListValue](left L, right R) *BinaryExpr {
	return &BinaryExpr{
		left:     leftOperand(left),
		operator: OpIn,
		right:    NewListLiteral(right),
	}
}

// Has tests whether a selected collection contains one complete scalar element.
// In tests a selected scalar against a supplied list.
func Has[L IdentifierValue | *IndexExpr, R LiteralValue](left L, right R) *BinaryExpr {
	return &BinaryExpr{
		left:     leftOperand(left),
		operator: OpHas,
		right:    NewLiteral(right),
	}
}

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
