package filter

import "testing"

type truth uint8

const (
	falseTruth truth = iota
	unknownTruth
	trueTruth
)

func TestOptimizerPreservesThreeValuedLogic(t *testing.T) {
	a := EQ("a", 1)
	b := EQ("b", 1)
	c := EQ("c", 1)

	tests := map[string]Predicate{
		"associative deduplication": And(And(a, b), a),
		"deep absorption":           And(a, Or(b, Or(c, a))),
		"commutative absorption":    And(Or(a, b), Or(b, Or(a, c))),
		"double negation":           Not(Not(a)),
		"factor conjunction":        Or(And(a, b), And(a, c)),
		"factor disjunction":        And(Or(a, b), Or(a, c)),
	}

	values := []truth{falseTruth, unknownTruth, trueTruth}
	for name, predicate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := predicate.Validate(); err != nil {
				t.Fatal(err)
			}
			optimized := optimize(predicate)
			if again := optimize(optimized); again != optimized {
				t.Fatal("optimizer did not reach a stable tree in one pass")
			}
			for _, av := range values {
				for _, bv := range values {
					for _, cv := range values {
						assignment := map[string]truth{"a": av, "b": bv, "c": cv}
						before := evaluateTruth(t, predicate, assignment)
						after := evaluateTruth(t, optimized, assignment)
						if before != after {
							t.Fatalf("assignment %v: before = %d, after = %d", assignment, before, after)
						}
					}
				}
			}
		})
	}
}

func TestOptimizerReusesCanonicalTree(t *testing.T) {
	predicate := And(EQ("a", 1), EQ("b", 1))
	if optimized := optimize(predicate); optimized != predicate {
		t.Fatal("optimizer rebuilt an already canonical tree")
	}
}

func TestOptimizerDoesNotMutateCallerTree(t *testing.T) {
	a := EQ("a", 1)
	b := EQ("b", 1)
	c := EQ("c", 1)
	left := And(a, b)
	right := And(a, c)
	predicate := Or(left, right)

	optimized := optimize(predicate)
	if predicate.left != left || predicate.right != right || left.left != a || right.left != a {
		t.Fatal("optimizer mutated the caller-owned boolean tree")
	}
	if !optimized.Equal(predicate) {
		t.Fatalf("optimized = %#v, want unchanged evaluation order", optimized)
	}
}

func TestLiteralIntegerIndexUsesExactArithmetic(t *testing.T) {
	tests := map[string]bool{
		"0":                   true,
		"9223372036854775807": true,
		"9223372036854775808": false,
		"9007199254740992.5":  false,
		"-1":                  false,
		"1e3":                 true,
	}
	for value, want := range tests {
		literal := &Literal{kind: LiteralNumber, text: value}
		if got := literal.isIntegerIndex(); got != want {
			t.Errorf("isIntegerIndex(%q) = %v, want %v", value, got, want)
		}
	}
}

func evaluateTruth(t *testing.T, predicate Predicate, assignment map[string]truth) truth {
	t.Helper()
	switch node := predicate.(type) {
	case *UnaryExpr:
		return trueTruth - evaluateTruth(t, node.right, assignment)
	case *BinaryExpr:
		if node.operator == OpAnd || node.operator == OpOr {
			left := evaluateTruth(t, node.left.(Predicate), assignment)
			right := evaluateTruth(t, node.right.(Predicate), assignment)
			if node.operator == OpAnd {
				return min(left, right)
			}
			return max(left, right)
		}
		ident, ok := node.left.(*Ident)
		if !ok {
			t.Fatalf("atomic predicate left operand = %T", node.left)
		}
		return assignment[ident.name]
	default:
		t.Fatalf("predicate = %T", predicate)
		return unknownTruth
	}
}
