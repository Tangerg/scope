package filter

// optimizer owns boolean-algebra normalization for trees already accepted by
// Validate. It is private: Parse exposes the normalized result, while public
// visitors receive programmatically built predicates unchanged.
type optimizer struct{}

func optimize(predicate Predicate) Predicate {
	return (optimizer{}).rewrite(predicate)
}

func (o optimizer) rewrite(predicate Predicate) Predicate {
	switch node := predicate.(type) {
	case *UnaryExpr:
		return o.rewriteUnary(node)
	case *BinaryExpr:
		return o.rewriteBinary(node)
	default:
		panic("filter: optimizer received an unvalidated predicate")
	}
}

func (o optimizer) rewriteUnary(unary *UnaryExpr) Predicate {
	right := o.rewrite(unary.right)
	if inner, ok := right.(*UnaryExpr); ok && unary.operator == OpNot && inner.operator == OpNot {
		return inner.right
	}
	if right == unary.right {
		return unary
	}
	return &UnaryExpr{
		operator: unary.operator, right: right,
		start: unary.start, end: unary.end,
	}
}

func (o optimizer) rewriteBinary(binary *BinaryExpr) Predicate {
	if !binary.operator.IsLogicalOperator() {
		return binary
	}

	left := o.rewrite(binary.left.(Predicate))
	right := o.rewrite(binary.right.(Predicate))

	terms := o.appendLogicalTerms(nil, binary.operator, left)
	terms = o.appendLogicalTerms(terms, binary.operator, right)
	terms, deduplicated := o.uniquePredicates(terms)
	if deduplicated {
		return o.joinLogical(binary.operator, terms)
	}

	if left == binary.left && right == binary.right {
		return binary
	}
	return &BinaryExpr{
		left: left, operator: binary.operator, right: right,
		start: binary.start, end: binary.end,
	}
}

func (o optimizer) appendLogicalTerms(terms []Predicate, operator Operator, predicate Predicate) []Predicate {
	binary, ok := predicate.(*BinaryExpr)
	if !ok || binary.operator != operator {
		return append(terms, predicate)
	}
	left, leftOK := binary.left.(Predicate)
	right, rightOK := binary.right.(Predicate)
	if !leftOK || !rightOK {
		return append(terms, predicate)
	}
	terms = o.appendLogicalTerms(terms, operator, left)
	return o.appendLogicalTerms(terms, operator, right)
}

func (o optimizer) uniquePredicates(predicates []Predicate) ([]Predicate, bool) {
	unique := make([]Predicate, 0, len(predicates))
	changed := false
	for _, candidate := range predicates {
		if o.containsPredicate(unique, candidate) {
			changed = true
			continue
		}
		unique = append(unique, candidate)
	}
	return unique, changed
}

func (optimizer) containsPredicate(predicates []Predicate, candidate Predicate) bool {
	for _, predicate := range predicates {
		if predicate.Equal(candidate) {
			return true
		}
	}
	return false
}

func (optimizer) joinLogical(operator Operator, predicates []Predicate) Predicate {
	if len(predicates) == 0 {
		panic("filter: cannot join an empty predicate set")
	}
	result := predicates[0]
	for _, right := range predicates[1:] {
		result = &BinaryExpr{
			left:     result,
			operator: operator,
			right:    right,
			start:    result.Start(),
			end:      right.End(),
		}
	}
	return result
}
