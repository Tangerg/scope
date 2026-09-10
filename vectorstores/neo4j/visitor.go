package neo4j

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a Cypher predicate
// string plus the matching parameter map. The output is intended to
// follow a `WHERE` clause in the search / delete statement.
//
// Output shape (using the default node alias "node" and metadata
// property prefix "metadata."):
//
//	author == "Alice"          →  node.`metadata.author` = $p1
//	year >= 2020               →  node.`metadata.year`   >= $p1
//	category IN ("a", "b")     →  node.`metadata.category` IN $p1
//	NOT (a == "x")             →  NOT (node.`metadata.a` = $p1)
//	a == "x" AND b == "y"      →  (node.`metadata.a` = $p1 AND node.`metadata.b` = $p2)
//
// Property paths follow convention: metadata keys are
// stored as flat node properties named `metadata.<key>` and addressed
// with backtick-quoted Cypher identifiers.
type visitor struct {
	err            error
	sql            strings.Builder
	params         map[string]any
	nodeAlias      string
	metadataPrefix string
}

func newVisitor(nodeAlias, metadataPrefix string) *visitor {
	if nodeAlias == "" {
		nodeAlias = "node"
	}
	return &visitor{
		nodeAlias:      nodeAlias,
		metadataPrefix: metadataPrefix,
		params:         make(map[string]any),
	}
}

func (v *visitor) snapshot() (string, map[string]any) {
	if v.err != nil {
		return "", nil
	}
	return v.sql.String(), v.params
}

func (v *visitor) Visit(expr filter.Predicate) error {
	v.sql.Reset()
	v.params = make(map[string]any)
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
			Like:       v.visitLikeExpr,
			NullTest:   v.visitNullTestExpr,
		})
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("neo4j: unsupported root expression %T", node)
	}
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	prop, err := v.propertyAccess(expr)
	if err != nil {
		return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
	}
	param := v.bindParam(value)
	v.sql.WriteString(param)
	v.sql.WriteString(" IN ")
	v.sql.WriteString(prop)
	return nil
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if !expr.Operator().Is(filter.OpNot) {
		return fmt.Errorf("neo4j: unsupported unary operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
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
	if expr.Operator().Is(filter.OpOr) {
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
	prop, err := v.propertyAccess(expr)
	if err != nil {
		return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
	}
	op, err := cypherOpFor(expr.Operator())
	if err != nil {
		return err
	}

	param := v.bindParam(value)
	v.sql.WriteString(prop)
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	v.sql.WriteString(param)
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	prop, err := v.propertyAccess(expr)
	if err != nil {
		return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
	}

	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return fmt.Errorf("neo4j: 'IN' requires a list on the right at %s, got %T",
			expr.Start().String(), expr.Right())
	}
	if listLit.Len() == 0 {
		return fmt.Errorf("neo4j: 'IN' requires a non-empty list at %s",
			expr.Start().String())
	}

	values := make([]any, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
		}
		values = append(values, val)
	}

	param := v.bindParam(values)
	v.sql.WriteString(prop)
	v.sql.WriteString(" IN ")
	v.sql.WriteString(param)
	return nil
}

// visitLikeExpr maps LIKE onto Cypher's regex operator =~. SQL
// wildcards translate to regex equivalents and the match is anchored.
func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	prop, err := v.propertyAccess(expr)
	if err != nil {
		return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
	}
	pattern, ok := value.(string)
	if !ok {
		return fmt.Errorf("neo4j: LIKE requires a string pattern, got %T at %s",
			value, expr.Start().String())
	}

	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteByte('.')
		case '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}

	param := v.bindParam(b.String())
	v.sql.WriteString(prop)
	v.sql.WriteString(" =~ ")
	v.sql.WriteString(param)
	return nil
}

// visitNullTestExpr emits `(node.`+"`metadata.key`"+` IS NULL)`. Cypher's
// IS NULL is true both when the property is absent and when it is
// explicitly null, matching the inmemory reference semantics. The
// negated `IS NOT NULL` arrives as NOT(… IS NULL) and is rendered by
// visitUnaryExpr as `NOT (… IS NULL)`, which Cypher treats as
// equivalent, so no separate handling is needed here. No bound
// parameter — `IS NULL` is inline in Cypher.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	prop, err := v.propertyAccess(expr)
	if err != nil {
		return fmt.Errorf("neo4j: %w (at %s)", err, expr.Start().String())
	}
	v.sql.WriteString("(")
	v.sql.WriteString(prop)
	v.sql.WriteString(" IS NULL)")
	return nil
}

// propertyAccess assembles the Cypher property accessor for the left
// side of a comparison, e.g. “node.`metadata.foo` “.
func (v *visitor) propertyAccess(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("neo4j: empty key path on left operand")
	}
	propName := strings.Join(keys, ".")
	if v.metadataPrefix != "" {
		propName = v.metadataPrefix + "." + propName
	}
	// Backtick-quote so dotted property names survive the Cypher
	// parser. A literal backtick inside the identifier is doubled
	// per Cypher's escaping rules.
	escaped := strings.ReplaceAll(propName, "`", "``")
	return v.nodeAlias + ".`" + escaped + "`", nil
}

func cypherOpFor(kind filter.Operator) (string, error) {
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
		return "", fmt.Errorf("neo4j: unexpected comparison operator '%s'", kind.Name())
	}
}

func (v *visitor) bindParam(value any) string {
	name := fmt.Sprintf("p%d", len(v.params)+1)
	v.params[name] = value
	return "$" + name
}
