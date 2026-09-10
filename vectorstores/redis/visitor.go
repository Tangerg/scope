package redis

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a RediSearch query
// fragment. The output is meant to be wrapped in parentheses and
// composed with a KNN vector tail by [Store].
//
// Output shape:
//
//	category == "tech"              →  @category:{tech}
//	category IN ("tech","ai")       →  @category:{tech|ai}
//	year >= 2020                    →  @year:[2020 +inf]
//	year == 2024                    →  @year:[2024 2024]
//	title LIKE "intro"              →  @title:(intro)
//	a == "x" AND b == "y"           →  (@a:{x} @b:{y})
//	a == "x" OR b == "y"            →  (@a:{x} | @b:{y})
//	NOT (a == "x")                  →  -(@a:{x})
//
// Field types come from [Store.fields] — keyed by metadata-field name —
// so the same operator dispatches to TAG / NUMERIC / TEXT syntax
// depending on the declared field kind.
type visitor struct {
	err    error
	sql    strings.Builder
	fields map[string]MetadataFieldType
}

func newVisitor(fields map[string]MetadataFieldType) *visitor {
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
		return node.Dispatch(filter.BinaryHandlers{
			Logical:    v.visitLogicalExpr,
			Comparison: v.visitComparisonExpr,
			In:         v.visitInExpr,
			Has:        v.visitHasExpr,
			Like:       v.visitTextFieldExpr,
		})
	case *filter.UnaryExpr:
		return node.Dispatch(v.visitNotExpr)
	default:
		return fmt.Errorf("redis: unsupported root expression %T", node)
	}
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	return fmt.Errorf("redis: HAS is not supported because the configured metadata schema exposes scalar TAG, NUMERIC, or TEXT fields (at %s)",
		expr.Start().String())
}

func (v *visitor) visitNotExpr(expr *filter.UnaryExpr) error {
	v.sql.WriteString("-(")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

// visitLogicalExpr uses RediSearch's space separator for AND and the
// pipe (` | `) for OR — not the verbatim "AND"/"OR" strings other
// vendors emit. We don't call Operator.LogicalString here because
// of that mapping difference.
func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	sep := " "
	if expr.Operator().Is(filter.OpOr) {
		sep = " | "
	}
	v.sql.WriteString("(")
	if err := v.visit(expr.Left()); err != nil {
		return err
	}
	v.sql.WriteString(sep)
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

// visitComparisonExpr dispatches based on the declared field type:
//   - TAG fields handle == and != (via NOT wrap)
//   - NUMERIC fields handle the full ordering set
//   - TEXT fields only support equality via the (value) syntax
func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	field, kind, err := v.resolveFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("redis: %w (at %s)", err, expr.Start().String())
	}

	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("redis: %w (at %s)", err, expr.Start().String())
	}

	op := expr.Operator()
	negate := op.Is(filter.OpNotEqual)
	if negate {
		op = filter.OpEqual
	}

	if negate {
		v.sql.WriteString("-")
	}

	switch kind {
	case FieldTag:
		if !op.Is(filter.OpEqual) {
			return fmt.Errorf("redis: TAG field '%s' only supports == / != / IN (got '%s')",
				field, expr.Operator().String())
		}
		v.sql.WriteString("@")
		v.sql.WriteString(field)
		v.sql.WriteString(":{")
		v.sql.WriteString(escapeTagValue(fmt.Sprint(value)))
		v.sql.WriteString("}")

	case FieldNumeric:
		literal, ok := expr.Right().(*filter.Literal)
		if !ok {
			return fmt.Errorf("redis: NUMERIC field '%s' requires a number literal, got %T", field, expr.Right())
		}
		num, err := literal.Float64()
		if err != nil {
			return fmt.Errorf("redis: NUMERIC field '%s': %w", field, err)
		}
		low, high := numericRange(op, num)
		v.sql.WriteString("@")
		v.sql.WriteString(field)
		v.sql.WriteString(":[")
		v.sql.WriteString(low)
		v.sql.WriteString(" ")
		v.sql.WriteString(high)
		v.sql.WriteString("]")

	case FieldText:
		if !op.Is(filter.OpEqual) {
			return fmt.Errorf("redis: TEXT field '%s' only supports == / != / LIKE (got '%s')",
				field, expr.Operator().String())
		}
		v.sql.WriteString("@")
		v.sql.WriteString(field)
		v.sql.WriteString(":(")
		v.sql.WriteString(escapeTextValue(fmt.Sprint(value)))
		v.sql.WriteString(")")

	default:
		return fmt.Errorf("redis: unsupported field type %q for '%s'", kind, field)
	}
	return nil
}

func (v *visitor) visitTextFieldExpr(expr *filter.BinaryExpr) error {
	field, kind, err := v.resolveFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("redis: %w (at %s)", err, expr.Start().String())
	}
	if kind != FieldText {
		return fmt.Errorf("redis: LIKE only supports TEXT fields, got %q for '%s'",
			kind, field)
	}

	pattern, err := expr.Pattern()
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}

	v.sql.WriteString("@")
	v.sql.WriteString(field)
	v.sql.WriteString(":(")
	v.sql.WriteString(redisWildcardPattern(pattern))
	v.sql.WriteString(")")
	return nil
}

// redisWildcardPattern emits Redis's w'...' wildcard-query form. SQL LIKE
// wildcards are translated while literal Redis wildcard metacharacters are
// escaped.
func redisWildcardPattern(pattern string) string {
	var out strings.Builder
	out.WriteString("w'")
	for _, char := range pattern {
		switch char {
		case '%':
			out.WriteByte('*')
		case '_':
			out.WriteByte('?')
		case '*', '?', '\\', '\'':
			out.WriteByte('\\')
			out.WriteRune(char)
		default:
			out.WriteRune(char)
		}
	}
	out.WriteByte('\'')
	return out.String()
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	field, kind, err := v.resolveFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("redis: %w (at %s)", err, expr.Start().String())
	}

	listLit, err := expr.List()
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}

	switch kind {
	case FieldTag:
		parts := make([]string, 0, listLit.Len())
		for _, lit := range listLit.Literals() {
			val, err := literalToString(lit)
			if err != nil {
				return fmt.Errorf("redis: %w (at %s)", err, expr.Start().String())
			}
			parts = append(parts, escapeTagValue(val))
		}
		v.sql.WriteString("@")
		v.sql.WriteString(field)
		v.sql.WriteString(":{")
		v.sql.WriteString(strings.Join(parts, "|"))
		v.sql.WriteString("}")

	case FieldText:
		parts := make([]string, 0, listLit.Len())
		for _, lit := range listLit.Literals() {
			val, err := literalToString(lit)
			if err != nil {
				return fmt.Errorf("redis: %w (at %s)", err, expr.Start().String())
			}
			parts = append(parts, escapeTextValue(val))
		}
		v.sql.WriteString("@")
		v.sql.WriteString(field)
		v.sql.WriteString(":(")
		v.sql.WriteString(strings.Join(parts, "|"))
		v.sql.WriteString(")")

	default:
		return fmt.Errorf("redis: IN is not supported on field type %q for '%s'",
			kind, field)
	}
	return nil
}

// resolveFieldKey extracts the metadata field name from the left side
// of a comparison and looks up its declared kind. Returns the bare
// identifier (no @prefix) so the caller controls the surrounding
// syntax.
func (v *visitor) resolveFieldKey(expr filter.Expr) (string, MetadataFieldType, error) {
	var field string
	var fieldType MetadataFieldType
	switch node := expr.(type) {
	case *filter.Ident:
		field = node.Name()
	case *filter.IndexExpr:
		// metadata["author"] / metadata["a"]["b"]: collapse the path
		// with dots — RediSearch fields are flat so deep paths must
		// be declared as dotted field names.
		parts, err := flattenIndexExpr(node)
		if err != nil {
			return "", fieldType, err
		}
		field = strings.Join(parts, ".")
	default:
		return "", fieldType, fmt.Errorf("redis: unsupported left operand %T", node)
	}

	kind, ok := v.fields[field]
	if !ok {
		return "", fieldType, fmt.Errorf("redis: filter references undeclared metadata field %q", field)
	}
	return field, kind, nil
}

// flattenIndexExpr collapses an [filter.IndexExpr] into a dotted path.
func flattenIndexExpr(expr *filter.IndexExpr) ([]string, error) {
	var keys []string
	current := expr
	for {
		key, err := current.Index().Key()
		if err != nil {
			return nil, err
		}
		keys = append([]string{key}, keys...)

		switch inner := current.Left().(type) {
		case *filter.IndexExpr:
			current = inner
		case *filter.Ident:
			keys = append([]string{inner.Name()}, keys...)
			return keys, nil
		default:
			return nil, fmt.Errorf("redis: unsupported index base %T", inner)
		}
	}
}

// numericRange builds the RediSearch [low high] bounds for an ordering
// operator. Inclusive bounds use the bare number; exclusive bounds use
// "(<n>".
func numericRange(op filter.Operator, value float64) (string, string) {
	v := formatNumber(value)
	switch op {
	case filter.OpEqual:
		return v, v
	case filter.OpGreater:
		return "(" + v, "+inf"
	case filter.OpGreaterEqual:
		return v, "+inf"
	case filter.OpLess:
		return "-inf", "(" + v
	case filter.OpLessEqual:
		return "-inf", v
	default:
		// Unreachable — visitComparisonExpr filtered the operator.
		return "-inf", "+inf"
	}
}

func formatNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func literalToString(lit *filter.Literal) (string, error) {
	switch {
	case lit.IsString():
		return lit.AsString()
	case lit.IsNumber():
		return lit.NumberText()
	case lit.IsBool():
		b, err := lit.AsBool()
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(b), nil
	default:
		return "", fmt.Errorf("redis: unsupported literal kind %s", lit.Kind())
	}
}

func escapeTagValue(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch r {
		case '\\', '$', '|', '{', '}', '(', ')', '[', ']', '-', '\'', ',', ' ', '.':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func escapeTextValue(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch r {
		case '\\', '$', '|', '{', '}', '(', ')', '[', ']', '"', '\'':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
