package elasticsearch

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into Elasticsearch
// query-string syntax (Lucene). The output is meant to be plugged
// into a `query_string.query` clause inside the KNN filter.
//
// Output shape (metadata fields are prefixed with the configured
// metadata field path — default "metadata"):
//
//	author == "Alice"          →  metadata.author:"Alice"
//	year >= 2020               →  metadata.year:>=2020
//	year < 2025                →  metadata.year:<2025
//	category IN ("a", "b")     →  metadata.category:("a" OR "b")
//	NOT (author == "Alice")    →  NOT (metadata.author:"Alice")
//	a == "x" AND b == "y"      →  (metadata.a:"x" AND metadata.b:"y")
//
// Identifier paths:
//   - bare identifier      → <prefix>.<ident>
//   - metadata['k']        → <prefix>.k
//   - metadata['a']['b']   → <prefix>.a.b
type visitor struct {
	err            error
	sql            strings.Builder
	metadataPrefix string // e.g. "metadata"
}

func newVisitor(metadataPrefix string) *visitor {
	return &visitor{metadataPrefix: metadataPrefix}
}

func (v *visitor) snapshot() string {
	if v.err != nil {
		return ""
	}
	return v.sql.String()
}

func (v *visitor) Visit(expr filter.Predicate) error {
	v.err = nil
	v.sql.Reset()
	v.err = v.visit(expr)
	return v.err
}

func (v *visitor) visit(expr filter.Expr) error {
	if expr == nil {
		return errors.New("elasticsearch: cannot process nil expression")
	}
	if v.err != nil {
		return v.err
	}

	switch node := expr.(type) {
	case *filter.BinaryExpr:
		if node.Operator().IsNullOperator() {
			return v.visitNullTestExpr(node)
		}
		return node.Dispatch(filter.BinaryHandlers{
			Logical:    v.visitLogicalExpr,
			Comparison: v.visitComparisonExpr,
			In:         v.visitInExpr,
			Has:        v.visitHasExpr,
			Like:       v.visitLikeExpr,
		})
	case *filter.UnaryExpr:
		return node.Dispatch(v.visitNotExpr)
	default:
		return fmt.Errorf("elasticsearch: unsupported root expression %T", node)
	}
}

// visitHasExpr uses Lucene's exact field query. Elasticsearch applies a term
// query to every value of a multi-valued field, so this is collection
// membership rather than scalar coercion at the provider boundary.
func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("elasticsearch: %w (at %s)", err, expr.Start().String())
	}
	lit, err := expr.Literal()
	if err != nil {
		return fmt.Errorf("elasticsearch: %w (at %s)", err, expr.Start().String())
	}
	term, err := formatLiteral(lit)
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteString(":")
	v.sql.WriteString(term)
	return nil
}

func (v *visitor) visitNotExpr(expr *filter.UnaryExpr) error {
	v.sql.WriteString("NOT (")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	op, err := expr.Operator().LogicalString()
	if err != nil {
		return fmt.Errorf("elasticsearch: %w", err)
	}
	v.sql.WriteString("(")
	if err := v.visit(expr.Left()); err != nil {
		return err
	}
	v.sql.WriteString(" ")
	v.sql.WriteString(op)
	v.sql.WriteString(" ")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("elasticsearch: %w (at %s)", err, expr.Start().String())
	}

	lit, err := expr.Literal()
	if err != nil {
		return fmt.Errorf("elasticsearch: %w (at %s)", err, expr.Start().String())
	}
	term, err := formatLiteral(lit)
	if err != nil {
		return err
	}

	switch expr.Operator() {
	case filter.OpEqual:
		v.sql.WriteString(field)
		v.sql.WriteString(":")
		v.sql.WriteString(term)
	case filter.OpNotEqual:
		v.sql.WriteString("NOT ")
		v.sql.WriteString(field)
		v.sql.WriteString(":")
		v.sql.WriteString(term)
	case filter.OpLess:
		v.sql.WriteString(field)
		v.sql.WriteString(":<")
		v.sql.WriteString(term)
	case filter.OpLessEqual:
		v.sql.WriteString(field)
		v.sql.WriteString(":<=")
		v.sql.WriteString(term)
	case filter.OpGreater:
		v.sql.WriteString(field)
		v.sql.WriteString(":>")
		v.sql.WriteString(term)
	case filter.OpGreaterEqual:
		v.sql.WriteString(field)
		v.sql.WriteString(":>=")
		v.sql.WriteString(term)
	default:
		return fmt.Errorf("elasticsearch: unexpected comparison operator '%s'", expr.Operator().String())
	}
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("elasticsearch: %w (at %s)", err, expr.Start().String())
	}

	listLit, err := expr.List()
	if err != nil {
		return fmt.Errorf("elasticsearch: %w", err)
	}

	parts := make([]string, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		term, err := formatLiteral(lit)
		if err != nil {
			return err
		}
		parts = append(parts, term)
	}

	v.sql.WriteString(field)
	v.sql.WriteString(":(")
	v.sql.WriteString(strings.Join(parts, " OR "))
	v.sql.WriteString(")")
	return nil
}

// visitNullTestExpr emits a "field is null" test as `NOT _exists_:<path>`.
// In Lucene query-string syntax `_exists_:<path>` matches documents where
// the field is present, so its negation matches absent (null) fields —
// matching the inmemory reference semantics where a missing or JSON-null
// metadata key is treated as null.
//
// The negated `IS NOT NULL` arrives as NOT(field IS NULL) and is rendered
// by visitNotExpr, which wraps this clause in another `NOT (...)`. The
// resulting `NOT (NOT _exists_:<path>)` is a double negation equivalent to
// `_exists_:<path>` — the existence check — so no separate handling is
// needed here.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("elasticsearch: %w (at %s)", err, expr.Start().String())
	}
	v.sql.WriteString("NOT _exists_:")
	v.sql.WriteString(field)
	return nil
}

// visitLikeExpr maps LIKE onto Lucene wildcard syntax. Right operand
// must be a string pattern — % is translated to *, _ to ?.
func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("elasticsearch: %w (at %s)", err, expr.Start().String())
	}

	pattern, err := expr.Pattern()
	if err != nil {
		return fmt.Errorf("elasticsearch: %w", err)
	}

	v.sql.WriteString(field)
	v.sql.WriteString(":")
	v.sql.WriteString(luceneWildcards(pattern))
	return nil
}

// fieldPath assembles the dotted Elasticsearch field path for the
// metadata key on the left side of a comparison.
func (v *visitor) fieldPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.IdentifierPath()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("empty key path on left operand")
	}
	return v.metadataPrefix + "." + strings.Join(keys, "."), nil
}

// formatLiteral renders a filter literal as a Lucene term.
//
// It reads the literal rather than a scalar decoded out of it because the
// literal owns the exact numeral and a scalar cannot carry it back. Deciding
// integer-ness with float64(int64(value)) == value asked Go for an
// out-of-range float-to-int conversion, which the spec leaves
// implementation-defined: at 2^63 arm64 saturates to MaxInt64, whose float64
// compares equal, so the term became 9223372036854775807 while amd64 emitted
// the right digits. An integer past int64 had no branch at all and fell
// through to a %v rendering nobody owned.
func formatLiteral(lit *filter.Literal) (string, error) {
	switch {
	case lit.IsString():
		text, err := lit.AsString()
		if err != nil {
			return "", fmt.Errorf("elasticsearch: %w (at %s)", err, lit.Start().String())
		}
		return `"` + escapeLuceneString(text) + `"`, nil
	case lit.IsNumber():
		text, err := lit.NumberText()
		if err != nil {
			return "", fmt.Errorf("elasticsearch: %w (at %s)", err, lit.Start().String())
		}
		return text, nil
	case lit.IsBool():
		value, err := lit.AsBool()
		if err != nil {
			return "", fmt.Errorf("elasticsearch: %w (at %s)", err, lit.Start().String())
		}
		return strconv.FormatBool(value), nil
	default:
		return "", fmt.Errorf("elasticsearch: unsupported literal kind '%s' at %s",
			lit.Kind(), lit.Start().String())
	}
}

func escapeLuceneString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// luceneWildcards maps SQL LIKE wildcards (% / _) to Lucene
// wildcards (* / ?) and escapes any pre-existing wildcards in the
// source pattern so they round-trip as literals.
func luceneWildcards(pattern string) string {
	var b strings.Builder
	b.Grow(len(pattern))
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteByte('*')
		case '_':
			b.WriteByte('?')
		case '*', '?':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
