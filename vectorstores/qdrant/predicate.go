package qdrant

import (
	"fmt"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

// Projecting scalar strings and non-null presence avoids Qdrant's array-any
// comparison and its numeric precision limits. Unsupported expressions never
// fall back to scrolling a collection or deleting by previously selected IDs.
func nativeFilter(predicate filter.Predicate) (*qdrantclient.Filter, error) {
	if predicate == nil {
		return nil, nil
	}
	if err := predicate.Validate(); err != nil {
		return nil, err
	}
	cond, err := nativeCondition(predicate)
	if err != nil {
		return nil, err
	}
	return &qdrantclient.Filter{Must: []*qdrantclient.Condition{cond}}, nil
}

func nativeCondition(expr filter.Expr) (*qdrantclient.Condition, error) {
	switch e := expr.(type) {
	case *filter.UnaryExpr:
		if e.Operator() == filter.OpNot {
			right, err := nativeCondition(e.Right())
			if err != nil {
				return nil, err
			}
			return mustNot(right), nil
		}
	case *filter.BinaryExpr:
		if e.Operator() == filter.OpAnd || e.Operator() == filter.OpOr {
			return nativeJunction(e)
		}
		condition, ok, err := keyword.Lower(e)
		if err != nil {
			return nil, err
		}
		if ok {
			term := qdrantclient.NewMatchKeyword(termsField, condition.Term)
			if condition.Negated {
				return mustNot(term), nil
			}
			return term, nil
		}
	}
	return nil, fmt.Errorf("%w: qdrant cannot lower %s", vectorstore.ErrUnsupportedFilter, expr)
}

func nativeJunction(e *filter.BinaryExpr) (*qdrantclient.Condition, error) {
	left, err := nativeCondition(e.Left())
	if err != nil {
		return nil, err
	}
	right, err := nativeCondition(e.Right())
	if err != nil {
		return nil, err
	}
	terms := []*qdrantclient.Condition{left, right}
	if e.Operator() == filter.OpAnd {
		return qdrantclient.NewFilterAsCondition(&qdrantclient.Filter{Must: terms}), nil
	}
	return qdrantclient.NewFilterAsCondition(&qdrantclient.Filter{Should: terms}), nil
}

func mustNot(condition *qdrantclient.Condition) *qdrantclient.Condition {
	return qdrantclient.NewFilterAsCondition(&qdrantclient.Filter{MustNot: []*qdrantclient.Condition{condition}})
}
