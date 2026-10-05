package vespa

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

type visitor struct {
	err    error
	sql    strings.Builder
	fields schemaFields
}

func newVisitor(fields schemaFields) *visitor {
	return &visitor{fields: fields}
}

func (v *visitor) snapshot() string {
	if v.err != nil {
		return ""
	}
	return v.sql.String()
}

func (v *visitor) Visit(expr filter.Predicate) error {
	v.sql.Reset()
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
		atomic := !node.Operator().IsLogicalOperator() && !node.Operator().IsNullOperator()
		if atomic {
			presence, err := v.presence(node)
			if err != nil {
				return err
			}
			// Unset native fields have defaults such as false or an empty string.
			// Presence is projected from Core metadata before any native coercion.
			v.sql.WriteString("(" + presence + " and ")
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
			v.sql.WriteByte(')')
		}
		return nil
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("vespa: unsupported root expression %T", node)
	}
}

func (v *visitor) presence(expr *filter.BinaryExpr) (string, error) {
	keys, err := v.selectorKeys(expr)
	if err != nil {
		return "", err
	}
	encoded, err := encodeMetadataPath(keys)
	if err != nil {
		return "", err
	}
	term, err := quoteYQLString(encoded)
	if err != nil {
		return "", err
	}
	return metadataPathsField + " contains " + term, nil
}

func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	presence, err := v.presence(expr)
	if err != nil {
		return err
	}
	v.sql.WriteString("!(" + presence + ")")
	return nil
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	lit, err := expr.Literal()
	if err != nil {
		return err
	}
	term, err := yqlLiteral(lit)
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteString(" contains ")
	v.sql.WriteString(term)
	return nil
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if expr.Operator() != filter.OpNot {
		return fmt.Errorf("vespa: unsupported unary '%s'", expr.Operator().String())
	}
	v.sql.WriteString("!(")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	op := " and "
	if expr.Operator() == filter.OpOr {
		op = " or "
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
	lit, err := expr.Literal()
	if err != nil {
		return err
	}
	return v.writeComparison(field, expr.Operator(), lit)
}

func (v *visitor) writeComparison(field string, operator filter.Operator, lit *filter.Literal) error {
	term, err := yqlLiteral(lit)
	if err != nil {
		return err
	}

	if lit.IsString() && operator == filter.OpEqual {
		v.sql.WriteString(field)
		v.sql.WriteString(" contains ")
		v.sql.WriteString(term)
		return nil
	}
	op, err := yqlOpFor(operator)
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	v.sql.WriteString(term)
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	literals, err := expr.List()
	if err != nil {
		return err
	}
	// Native IN coerces numeric members to integers and excludes bool fields.
	// Core membership is a disjunction of the same scalar equality it owns.
	v.sql.WriteByte('(')
	for index, literal := range literals.Literals() {
		if index != 0 {
			v.sql.WriteString(" or ")
		}
		if err := v.writeComparison(field, filter.OpEqual, literal); err != nil {
			return err
		}
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
	// Native regex matching searches substrings. Core matches the whole value,
	// and its wildcards include newlines and count Unicode characters.
	escaped := regexp.QuoteMeta(pattern)
	wildcards := strings.NewReplacer("%", ".*", "_", ".").Replace(escaped)
	term, err := quoteYQLString("(?s)^" + wildcards + "$")
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteString(" matches ")
	v.sql.WriteString(term)
	return nil
}

func (v *visitor) fieldPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := v.selectorKeys(expr)
	if err != nil {
		return "", err
	}
	return strings.Join(keys, "."), nil
}

func (v *visitor) selectorKeys(expr *filter.BinaryExpr) ([]string, error) {
	keys, err := expr.IdentifierPath()
	if err != nil {
		return nil, err
	}
	if v.fields.reserved(keys[0]) {
		return nil, fmt.Errorf("vespa: metadata key %q is reserved", keys[0])
	}
	return keys, nil
}

func yqlOpFor(kind filter.Operator) (string, error) {
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
		return "", fmt.Errorf("vespa: unexpected operator '%s'", kind.Name())
	}
}

// Native YQL parses integer tokens as signed 64-bit values. Core's literal owns
// the exact numeral, so overflow is refused before I/O rather than rounded.
func yqlLiteral(lit *filter.Literal) (string, error) {
	switch {
	case lit.IsString():
		text, err := lit.AsString()
		if err != nil {
			return "", fmt.Errorf("vespa: %w (at %s)", err, lit.Start().String())
		}
		return quoteYQLString(text)
	case lit.IsNumber():
		text, err := lit.NumberText()
		if err != nil {
			return "", fmt.Errorf("vespa: %w (at %s)", err, lit.Start().String())
		}
		if !strings.ContainsRune(text, '.') {
			if _, err := lit.AsInt64(); err != nil {
				return "", fmt.Errorf("vespa: native integer literal %s: %w", text, err)
			}
		}
		return text, nil
	case lit.IsBool():
		value, err := lit.AsBool()
		if err != nil {
			return "", fmt.Errorf("vespa: %w (at %s)", err, lit.Start().String())
		}
		return strconv.FormatBool(value), nil
	default:
		return "", fmt.Errorf("vespa: unsupported literal kind '%s' at %s",
			lit.Kind(), lit.Start().String())
	}
}

func quoteYQLString(value string) (string, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("vespa: encode string literal: %w", err)
	}
	return string(encoded), nil
}
