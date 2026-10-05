package azurecosmos

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

type visitor struct {
	err            error
	sql            strings.Builder
	params         []azcosmos.QueryParameter
	alias          string
	metadataPrefix string
}

func newVisitor(alias, metadataPrefix string) *visitor {
	if alias == "" {
		alias = "c"
	}
	return &visitor{alias: alias, metadataPrefix: metadataPrefix}
}

func (v *visitor) snapshot() (string, []azcosmos.QueryParameter) {
	if v.err != nil {
		return "", nil
	}
	return v.sql.String(), v.params
}

func (v *visitor) Visit(expr filter.Predicate) error {
	v.sql.Reset()
	v.params = nil
	v.err = v.visit(expr)
	return v.err
}

func (v *visitor) visit(expr filter.Expr) error {
	switch node := expr.(type) {
	case *filter.BinaryExpr:
		if node.Operator() == filter.OpNotEqual {
			equality, err := node.Inverse()
			if err != nil {
				return err
			}
			return v.visit(filter.Not(equality))
		}
		// Core predicates are two-valued. Resolve native undefined before NOT
		// or a compound predicate can turn an absent field into a lost match.
		atomic := !node.Operator().IsLogicalOperator() && !node.Operator().IsNullOperator()
		if atomic {
			v.sql.WriteString("((")
		}
		if err := node.Dispatch(filter.BinaryHandlers{
			Logical:    v.visitLogicalExpr,
			Comparison: v.visitComparisonExpr,
			In:         v.visitInExpr,
			Has:        v.visitHasExpr,
			Like:       v.visitLikeExpr,
			NullTest:   v.visitNullTestExpr,
		}); err != nil {
			return err
		}
		if atomic {
			v.sql.WriteString(") ?? false)")
		}
		return nil
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("azurecosmos: unsupported root expression %T", node)
	}
}

// visitNullTestExpr emits `(NOT IS_DEFINED(<path>) OR IS_NULL(<path>))`.
//
// IS_NULL alone is not enough: the documented example evaluates
// IS_NULL({quantity: 25, vendor: null}.size) to false, so an absent property
// is not null to Cosmos — while it is nil to the filter AST, and absent is the
// ordinary case for metadata. IS_DEFINED separates the two states, so the
// disjunction covers exactly the ones the AST reads as nil. The negated IS NOT
// NULL arrives as NOT(...) and is rendered by visitUnaryExpr.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	v.sql.WriteString("(NOT IS_DEFINED(")
	v.sql.WriteString(field)
	v.sql.WriteString(") OR IS_NULL(")
	v.sql.WriteString(field)
	v.sql.WriteString("))")
	return nil
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	value, err := expr.Value()
	if err != nil {
		return err
	}
	v.sql.WriteString("ARRAY_CONTAINS(")
	v.sql.WriteString(field)
	v.sql.WriteString(", ")
	v.sql.WriteString(v.bindParam(value))
	v.sql.WriteByte(')')
	return nil
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if expr.Operator() != filter.OpNot {
		return fmt.Errorf("azurecosmos: unsupported unary '%s'", expr.Operator().String())
	}
	v.sql.WriteString("NOT (")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	op := " AND "
	if expr.Operator() == filter.OpOr {
		op = " OR "
	}
	v.sql.WriteString("(")
	if err := v.visit(expr.Left()); err != nil {
		return err
	}
	v.sql.WriteString(op)
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	value, err := expr.Value()
	if err != nil {
		return err
	}
	op, err := sqlOpFor(expr.Operator())
	if err != nil {
		return err
	}
	param := v.bindParam(value)
	v.sql.WriteString(field)
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	v.sql.WriteString(param)
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return errors.New("azurecosmos: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return errors.New("azurecosmos: 'IN' requires a non-empty list")
	}

	v.sql.WriteString(field)
	v.sql.WriteString(" IN (")
	for i, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return err
		}
		if i > 0 {
			v.sql.WriteString(", ")
		}
		v.sql.WriteString(v.bindParam(val))
	}
	v.sql.WriteByte(')')
	return nil
}

func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	pattern, err := expr.Pattern()
	if err != nil {
		return err
	}
	// Core treats brackets and the native escape character as literals.
	pattern = strings.ReplaceAll(strings.ReplaceAll(pattern, "!", "!!"), "[", "![")
	v.sql.WriteString(field)
	v.sql.WriteString(" LIKE ")
	v.sql.WriteString(v.bindParam(pattern))
	v.sql.WriteString(" ESCAPE '!'")
	return nil
}

func (v *visitor) fieldPath(expr *filter.BinaryExpr) (string, error) {
	selector, err := expr.Selector()
	if err != nil {
		return "", err
	}
	path, err := cosmosSelectorPath(selector)
	if err != nil {
		return "", err
	}
	prefix := v.alias
	if v.metadataPrefix != "" {
		prefix += "." + v.metadataPrefix
	}
	return prefix + path, nil
}

func cosmosSelectorPath(selector filter.Selector) (string, error) {
	switch node := selector.(type) {
	case *filter.Ident:
		return "." + node.Name(), nil
	case *filter.IndexExpr:
		parent, err := cosmosSelectorPath(node.Left())
		if err != nil {
			return "", err
		}
		if node.Index().IsString() {
			key, keyErr := node.Index().AsString()
			if keyErr != nil {
				return "", keyErr
			}
			quoted, marshalErr := jsonv2.Marshal(key)
			if marshalErr != nil {
				return "", marshalErr
			}
			return parent + "[" + string(quoted) + "]", nil
		}
		index, err := node.Index().AsInt64()
		if err != nil || index < 0 {
			return "", fmt.Errorf("invalid array index %s", node.Index().Text())
		}
		return fmt.Sprintf("%s[%d]", parent, index), nil
	default:
		return "", fmt.Errorf("unsupported selector %T", selector)
	}
}

func sqlOpFor(kind filter.Operator) (string, error) {
	switch kind {
	case filter.OpEqual:
		return "=", nil
	case filter.OpLess:
		return "<", nil
	case filter.OpLessEqual:
		return "<=", nil
	case filter.OpGreater:
		return ">", nil
	case filter.OpGreaterEqual:
		return ">=", nil
	default:
		return "", fmt.Errorf("azurecosmos: unexpected operator '%s'", kind.Name())
	}
}

func (v *visitor) bindParam(value any) string {
	name := fmt.Sprintf("@p%d", len(v.params)+1)
	v.params = append(v.params, azcosmos.QueryParameter{Name: name, Value: value})
	return name
}
