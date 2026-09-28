package filter

func logic[L Predicate, R Predicate](left L, right R, operator Operator) *BinaryExpr {
	return &BinaryExpr{
		left:     left,
		operator: operator,
		right:    right,
	}
}

func And[L Predicate, R Predicate](left L, right R) *BinaryExpr {
	return logic(left, right, OpAnd)
}

func Or[L Predicate, R Predicate](left L, right R) *BinaryExpr {
	return logic(left, right, OpOr)
}

func Not[T Predicate](predicate T) *UnaryExpr {
	return &UnaryExpr{
		operator: OpNot,
		right:    predicate,
	}
}
