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
			return qdrantclient.NewFilterAsCondition(&qdrantclient.Filter{MustNot: []*qdrantclient.Condition{right}}), nil
		}
	case *filter.BinaryExpr:
		switch e.Operator() {
		case filter.OpAnd, filter.OpOr:
			l, err := nativeCondition(e.Left())
			if err != nil {
				return nil, err
			}
			r, err := nativeCondition(e.Right())
			if err != nil {
				return nil, err
			}
			terms := []*qdrantclient.Condition{l, r}
			if e.Operator() == filter.OpAnd {
				return qdrantclient.NewFilterAsCondition(&qdrantclient.Filter{Must: terms}), nil
			}
			return qdrantclient.NewFilterAsCondition(&qdrantclient.Filter{Should: terms}), nil
		case filter.OpEqual, filter.OpNotEqual, filter.OpIs:
			path, err := e.Path()
			if err != nil || len(path) != 1 {
				break
			}
			key, ok := path[0].Key()
			if !ok {
				break
			}
			var term string
			negate := false
			if e.Operator() == filter.OpIs {
				term = keyword.Presence(key)
				negate = true
			} else {
				v, err := e.Value()
				if err != nil {
					return nil, err
				}
				str, ok := v.(string)
				if !ok {
					break
				}
				term = keyword.String(key, str)
				negate = e.Operator() == filter.OpNotEqual
			}
			c := qdrantclient.NewMatchKeyword(termsField, term)
			if negate {
				return qdrantclient.NewFilterAsCondition(&qdrantclient.Filter{MustNot: []*qdrantclient.Condition{c}}), nil
			}
			return c, nil
		}
	}
	return nil, fmt.Errorf("%w: qdrant cannot lower %s", vectorstore.ErrUnsupportedFilter, expr)
}
