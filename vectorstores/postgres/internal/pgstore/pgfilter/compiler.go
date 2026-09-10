package pgfilter

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Compiler transforms AST filter expressions into a parameterized
// PostgreSQL WHERE-clause fragment plus the matching argument list.
//
// Output shape (using the default metadata column "metadata"):
//
//	author == "Alice"        →  (metadata->>'author' = $1)
//	year >= 2020             →  ((metadata->>'year')::numeric >= $1)
//	published == true        →  ((metadata->>'published')::boolean = $1)
//	tag IN ("rag","llm")     →  (metadata->>'tag' = ANY($1))
//	NOT (a == 1)             →  (NOT (metadata->>'a')::numeric = $1)
//
// Identifier paths:
//   - simple identifier — used as the top-level metadata key:
//     author → metadata->>'author'
//   - indexed expression keeps the base identifier as the first path segment:
//     profile["author"] → metadata->'profile'->>'author'
//   - nested index — joined with -> for intermediate hops,
//     ->> only on the final step (since ->> casts to text):
//     profile["a"]["b"] → metadata->'profile'->'a'->>'b'
//
// Numeric / boolean values force a type cast on the JSON extraction so
// the comparison happens in the proper type, not lexicographic on text.
var _ filter.Visitor = (*Compiler)(nil)

// Compiler translates a portable filter AST into a parameterized SQL predicate.
// It emits placeholders rather than literals so a filter value carried from a
// model or a request cannot alter the statement it is filtering.
type Compiler struct {
	err         error
	sql         strings.Builder
	args        []any
	metadataCol string // SQL identifier — already validated by the caller
}

// NewCompiler binds the metadata column once, because every path expression in
// a filter is rooted at that column and repeating it per call would let two
// statements disagree about where metadata lives.
func NewCompiler(metadataCol string) *Compiler {
	if metadataCol == "" {
		metadataCol = "metadata"
	}
	return &Compiler{metadataCol: metadataCol}
}

func (c *Compiler) Result() (string, []any) {
	if c.err != nil {
		return "", nil
	}
	return c.sql.String(), c.args
}

func (c *Compiler) Visit(expr filter.Predicate) error {
	c.sql.Reset()
	c.args = nil
	c.err = c.visit(expr)
	return c.err
}

func (c *Compiler) visit(expr filter.Expr) error {
	switch node := expr.(type) {
	case *filter.BinaryExpr:
		return node.Dispatch(filter.BinaryHandlers{
			Logical:    c.visitLogicalExpr,
			Comparison: c.visitComparisonExpr,
			In:         c.visitInExpr,
			Has:        c.visitHasExpr,
			Like:       c.visitLikeExpr,
			NullTest:   c.visitNullTestExpr,
		})
	case *filter.UnaryExpr:
		return node.Dispatch(c.visitNotExpr)
	default:
		return fmt.Errorf("pgvector: unsupported root expression type %T", node)
	}
}

// visitHasExpr uses JSONB containment against the collection at the selected
// metadata path. jsonb_build_array preserves the scalar parameter's JSON type.
func (c *Compiler) visitHasExpr(expr *filter.BinaryExpr) error {
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}
	jsonPath, err := buildRawJSONPath(expr, c.metadataCol)
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}

	c.args = append(c.args, value)
	c.sql.WriteString("(")
	c.sql.WriteString(jsonPath)
	c.sql.WriteString(" @> jsonb_build_array($")
	c.sql.WriteString(strconv.Itoa(len(c.args)))
	c.sql.WriteString("))")
	return nil
}

func (c *Compiler) visitNotExpr(expr *filter.UnaryExpr) error {
	c.sql.WriteString("(NOT ")
	if err := c.visit(expr.Right()); err != nil {
		return err
	}
	c.sql.WriteString(")")
	return nil
}

func (c *Compiler) visitLogicalExpr(expr *filter.BinaryExpr) error {
	op, err := expr.Operator().LogicalString()
	if err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}

	c.sql.WriteString("(")
	if err := c.visit(expr.Left()); err != nil {
		return err
	}
	c.sql.WriteString(" ")
	c.sql.WriteString(op)
	c.sql.WriteString(" ")
	if err := c.visit(expr.Right()); err != nil {
		return err
	}
	c.sql.WriteString(")")
	return nil
}

// writeAbsentGuard prefixes a leaf predicate with the truth value the filter
// AST assigns an absent metadata key, so the leaf is never UNKNOWN.
//
// The AST is two-valued: an absent key evaluates as nil and filter.Match
// decides every comparison against it — false for ==, <, LIKE and IN, true for
// !=. SQL is three-valued, so a bare comparison on a missing key is UNKNOWN,
// which drops the row for any operator and, worse, stays UNKNOWN under NOT,
// dropping the rows a negated filter is supposed to keep. Anchoring each leaf
// restores two-valued behavior for the whole expression, negation included.
//
// The guard reads the uncast extraction on purpose: a cast is applied to
// compare, and testing the raw text for NULL keeps the guard independent of
// whether that cast succeeds.
func (c *Compiler) writeAbsentGuard(expr *filter.BinaryExpr, absentMatches bool) error {
	rawPath, err := buildJSONPath(expr, c.metadataCol, castNone)
	if err != nil {
		return err
	}
	c.sql.WriteString(rawPath)
	if absentMatches {
		c.sql.WriteString(" IS NULL OR ")
	} else {
		c.sql.WriteString(" IS NOT NULL AND ")
	}
	return nil
}

// visitComparisonExpr handles ==, !=, <, <=, >, >=. The JSON extraction
// expression on the left side is type-cast based on the value type:
// numbers → ::numeric, bools → ::boolean, strings → no cast.
func (c *Compiler) visitComparisonExpr(expr *filter.BinaryExpr) error {
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}

	jsonPath, err := buildJSONPath(expr, c.metadataCol, comparisonCastFor(value, expr.Operator()))
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}

	op, err := sqlOpFor(expr.Operator())
	if err != nil {
		return err
	}

	c.args = append(c.args, value)
	c.sql.WriteString("(")
	if err := c.writeAbsentGuard(expr, expr.Operator() == filter.OpNotEqual); err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}
	c.sql.WriteString(jsonPath)
	c.sql.WriteString(" ")
	c.sql.WriteString(op)
	c.sql.WriteString(" $")
	c.sql.WriteString(strconv.Itoa(len(c.args)))
	c.sql.WriteString(")")
	return nil
}

// visitInExpr emits `key = ANY($N)` with a slice argument. Element type
// follows the literal type — pgx maps Go slices to a Postgres array of
// the matching type.
func (c *Compiler) visitInExpr(expr *filter.BinaryExpr) error {
	listLit, err := expr.List()
	if err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}

	values, err := listLit.Values()
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}
	first, err := listLit.First()
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}
	sample, err := first.Value()
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}

	jsonPath, err := buildJSONPath(expr, c.metadataCol, comparisonCastFor(sample, filter.OpEqual))
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}

	c.args = append(c.args, values)
	c.sql.WriteString("(")
	if err := c.writeAbsentGuard(expr, false); err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}
	c.sql.WriteString(jsonPath)
	c.sql.WriteString(" = ANY($")
	c.sql.WriteString(strconv.Itoa(len(c.args)))
	c.sql.WriteString("))")
	return nil
}

// visitLikeExpr emits a SQL LIKE. ILIKE would be a wider match than the
// operator asks for: LIKE is case-sensitive, which is exactly why Postgres
// ships ILIKE as a separate keyword, and filter.Match compares case-sensitively
// too. The right side must be a string literal.
func (c *Compiler) visitLikeExpr(expr *filter.BinaryExpr) error {
	pattern, err := expr.Pattern()
	if err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}

	jsonPath, err := buildJSONPath(expr, c.metadataCol, castNone)
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}

	c.args = append(c.args, pattern)
	c.sql.WriteString("(")
	if err := c.writeAbsentGuard(expr, false); err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}
	c.sql.WriteString(jsonPath)
	c.sql.WriteString(" LIKE $")
	c.sql.WriteString(strconv.Itoa(len(c.args)))
	c.sql.WriteString(")")
	return nil
}

// visitNullTestExpr emits `(metadata->>'key' IS NULL)`. Postgres `->>`
// yields SQL NULL both when the key is absent and when the stored value
// is JSON null, matching the inmemory reference semantics. The negated
// `IS NOT NULL` arrives as NOT(… IS NULL) and is rendered by
// visitNotExpr, so no separate handling is needed here.
func (c *Compiler) visitNullTestExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr, c.metadataCol, castNone)
	if err != nil {
		return fmt.Errorf("pgvector: %w (at %s)", err, expr.Start().String())
	}
	c.sql.WriteString("(")
	c.sql.WriteString(jsonPath)
	c.sql.WriteString(" IS NULL)")
	return nil
}

// jsonCast names the Postgres type cast applied to the JSON
// extraction. castNone returns the raw text from ->>.
type jsonCast int

const (
	castNone jsonCast = iota
	castNumeric
	castBoolean
)

func comparisonCastFor(value any, op filter.Operator) jsonCast {
	switch value.(type) {
	case bool:
		return castBoolean
	case float64, int, int64, uint64:
		return castNumeric
	default:
		// Ordering on non-numeric values still falls back to a
		// numeric cast — the user asked for an ordering comparison,
		// so coerce.
		if op.IsOrderingOperator() {
			return castNumeric
		}
		return castNone
	}
}

func sqlOpFor(kind filter.Operator) (string, error) {
	switch kind {
	case filter.OpEqual:
		return "=", nil
	case filter.OpNotEqual:
		return "<>", nil
	case filter.OpLess:
		return "<", nil
	case filter.OpLessEqual:
		return "<=", nil
	case filter.OpGreater:
		return ">", nil
	case filter.OpGreaterEqual:
		return ">=", nil
	default:
		return "", fmt.Errorf("pgvector: unexpected comparison operator '%s'", kind.Name())
	}
}

// buildJSONPath turns the left-side expression of a comparison into
// the metadata accessor.
//
//	ident            → metadata->>'ident'
//	metadata['k']    → metadata->>'k'
//	metadata['a']['b'] → metadata->'a'->>'b'
//
// For numeric / boolean comparisons the trailing ->> is wrapped in a
// type cast.
func buildJSONPath(expr *filter.BinaryExpr, metadataCol string, cast jsonCast) (string, error) {
	pathParts, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(pathParts) == 0 {
		return "", errors.New("empty key path on left operand")
	}

	var b strings.Builder
	if cast != castNone {
		b.WriteString("(")
	}
	b.WriteString(metadataCol)

	for i, key := range pathParts {
		if i == len(pathParts)-1 {
			b.WriteString("->>")
		} else {
			b.WriteString("->")
		}
		b.WriteString(quoteSQLLiteral(key))
	}

	if cast != castNone {
		b.WriteString(")")
		switch cast {
		case castNumeric:
			b.WriteString("::numeric")
		case castBoolean:
			b.WriteString("::boolean")
		}
	}
	return b.String(), nil
}

// buildRawJSONPath keeps the selected value as JSONB. Collection operators
// must not use ->>, which would erase the array shape by converting it to text.
func buildRawJSONPath(expr *filter.BinaryExpr, metadataCol string) (string, error) {
	pathParts, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(pathParts) == 0 {
		return "", errors.New("empty key path on left operand")
	}

	var b strings.Builder
	b.WriteString(metadataCol)
	for _, key := range pathParts {
		b.WriteString("->")
		b.WriteString(quoteSQLLiteral(key))
	}
	return b.String(), nil
}

func quoteSQLLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
