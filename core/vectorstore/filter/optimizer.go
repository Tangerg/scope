package filter

// Only trees returned by Parse are normalized; programmatically constructed
// predicates reach visitors unchanged.
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

// rewriteBinary normalizes one maximal same-operator group exactly once. It
// flattens the whole group, rewrites each operand, and deduplicates the result a
// single time. Flattening penetrates eliminable double negations, so a
// same-operator subgroup hidden behind NOT NOT joins this one work unit instead
// of being deduplicated again by its own rewrite: a chain of n operands costs
// O(n^2) comparisons rather than O(n^3), even when every link is wrapped in a
// double negation. A non-logical binary passes through.
func (o optimizer) rewriteBinary(binary *BinaryExpr) Predicate {
	if !binary.operator.IsLogicalOperator() {
		return binary
	}

	operands := o.appendLogicalTerms(nil, binary.operator, binary)
	rewritten := make([]Predicate, len(operands))
	for index, operand := range operands {
		rewritten[index] = o.rewrite(operand)
	}

	var terms []Predicate
	for _, operand := range rewritten {
		terms = o.appendLogicalTerms(terms, binary.operator, operand)
	}
	unique, deduplicated := o.uniquePredicates(terms)
	if deduplicated {
		return o.joinLogical(binary.operator, unique)
	}

	// rebuildGroup returns binary unchanged when no operand moved and no double
	// negation collapsed, so the identity flag the caller once tracked is implicit.
	index := 0
	return o.rebuildGroup(binary, rewritten, &index)
}

// appendLogicalTerms collects the operands of the maximal subtree joined by
// operator. It descends through same-operator binaries and penetrates eliminable
// double negations, so NOT NOT (X and Y) contributes X and Y to an AND group
// rather than a single nested operand. A non-eliminable NOT or a different
// operator stays one operand.
func (o optimizer) appendLogicalTerms(terms []Predicate, operator Operator, predicate Predicate) []Predicate {
	predicate = stripDoubleNegations(predicate)
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

// stripDoubleNegations removes paired NOT operators, which cancel, returning the
// first predicate that is not a double negation. An odd negation keeps its
// outermost NOT.
func stripDoubleNegations(predicate Predicate) Predicate {
	for {
		outer, ok := predicate.(*UnaryExpr)
		if !ok || outer.operator != OpNot {
			return predicate
		}
		inner, ok := outer.right.(*UnaryExpr)
		if !ok || inner.operator != OpNot {
			return predicate
		}
		predicate = inner.right
	}
}

// rebuildGroup rebuilds the same-operator group rooted at binary, substituting
// each operand in flatten order with operands[*index] while preserving the
// original shape and source positions. A group whose only change is a rewritten
// operand keeps its structure instead of collapsing to a canonical left-deep
// chain, and an unchanged group returns binary itself. Its descent mirrors
// appendLogicalTerms, penetrating the same double negations, so the operand
// indexing stays aligned and a collapsed NOT NOT drops out of the rebuilt tree.
func (o optimizer) rebuildGroup(binary *BinaryExpr, operands []Predicate, index *int) Predicate {
	left := o.rebuildSide(binary.left.(Predicate), binary.operator, operands, index)
	right := o.rebuildSide(binary.right.(Predicate), binary.operator, operands, index)
	if left == binary.left && right == binary.right {
		return binary
	}
	return &BinaryExpr{
		left: left, operator: binary.operator, right: right,
		start: binary.start, end: binary.end,
	}
}

func (o optimizer) rebuildSide(side Predicate, operator Operator, operands []Predicate, index *int) Predicate {
	if inner, ok := stripDoubleNegations(side).(*BinaryExpr); ok && inner.operator == operator {
		_, leftOK := inner.left.(Predicate)
		_, rightOK := inner.right.(Predicate)
		if leftOK && rightOK {
			return o.rebuildGroup(inner, operands, index)
		}
	}
	operand := operands[*index]
	*index++
	return operand
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
