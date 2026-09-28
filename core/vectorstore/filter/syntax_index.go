package filter

// Index selects a nested map key or array element. Numeric indices must be non-negative integers.
func Index[L IdentifierValue | *IndexExpr, I Number | string | *Literal](left L, index I) *IndexExpr {
	return &IndexExpr{left: leftOperand(left), index: NewLiteral(index)}
}
