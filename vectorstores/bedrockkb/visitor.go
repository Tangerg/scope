package bedrockkb

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// visitor compiles Scope filter expressions into Bedrock retrieval filters.
// Bedrock Knowledge Bases address metadata keys directly by name; nested paths
// are not supported, so the left operand must be a bare identifier.
type visitor struct {
	result types.RetrievalFilter
}

var _ filter.Visitor = (*visitor)(nil)

func newVisitor() *visitor {
	return &visitor{}
}

// Visit compiles the complete expression tree rooted at predicate.
func (v *visitor) Visit(predicate filter.Predicate) error {
	v.result = nil
	result, err := v.convertExpr(predicate)
	if err != nil {
		return err
	}
	v.result = result
	return nil
}

// Failed compilation clears the prior value so a reused compiler cannot leak a stale filter.
func (v *visitor) snapshot() types.RetrievalFilter {
	return v.result
}

func (v *visitor) convertExpr(expr filter.Expr) (types.RetrievalFilter, error) {
	switch node := expr.(type) {
	case *filter.BinaryExpr:
		return v.convertBinary(node)
	case *filter.UnaryExpr:
		return v.convertUnary(node)
	default:
		return nil, fmt.Errorf("bedrockkb: unsupported root expression %T", node)
	}
}

func (v *visitor) convertBinary(expr *filter.BinaryExpr) (types.RetrievalFilter, error) {
	switch {
	case expr.Operator().Is(filter.OpAnd), expr.Operator().Is(filter.OpOr):
		return v.convertLogical(expr)
	case expr.Operator().Is(filter.OpIn):
		return v.convertIn(expr)
	case expr.Operator().Is(filter.OpHas):
		return v.convertHas(expr)
	case expr.Operator().Is(filter.OpLike):
		return v.convertLike(expr)
	case expr.Operator().IsEqualityOperator() || expr.Operator().IsOrderingOperator():
		return v.convertComparison(expr)
	default:
		return nil, fmt.Errorf("bedrockkb: unsupported binary operator '%s'", expr.Operator().String())
	}
}

// convertUnary handles NOT by rewriting the negated child into its
// inverse, since Bedrock has no top-level NOT filter member.
func (v *visitor) convertUnary(expr *filter.UnaryExpr) (types.RetrievalFilter, error) {
	if !expr.Operator().Is(filter.OpNot) {
		return nil, fmt.Errorf("bedrockkb: unsupported unary '%s'", expr.Operator().String())
	}
	bin, ok := expr.Right().(*filter.BinaryExpr)
	if !ok {
		return nil, errors.New("bedrockkb: NOT may only wrap a binary comparison")
	}
	if bin.Operator().Is(filter.OpIn) {
		return v.convertNotIn(bin)
	}
	inverted, err := invertBinary(bin)
	if err != nil {
		return nil, err
	}
	return v.convertExpr(inverted)
}

func (v *visitor) convertLogical(expr *filter.BinaryExpr) (types.RetrievalFilter, error) {
	left, err := v.convertExpr(expr.Left())
	if err != nil {
		return nil, err
	}
	right, err := v.convertExpr(expr.Right())
	if err != nil {
		return nil, err
	}
	if expr.Operator().Is(filter.OpOr) {
		return &types.RetrievalFilterMemberOrAll{Value: []types.RetrievalFilter{left, right}}, nil
	}
	return &types.RetrievalFilterMemberAndAll{Value: []types.RetrievalFilter{left, right}}, nil
}

func (v *visitor) convertComparison(expr *filter.BinaryExpr) (types.RetrievalFilter, error) {
	key, err := keyName(expr.Left())
	if err != nil {
		return nil, err
	}
	value, err := extractLiteralValue(expr.Right())
	if err != nil {
		return nil, err
	}
	attr := types.FilterAttribute{
		Key:   &key,
		Value: document.NewLazyDocument(value),
	}
	switch expr.Operator() {
	case filter.OpEqual:
		return &types.RetrievalFilterMemberEquals{Value: attr}, nil
	case filter.OpNotEqual:
		return &types.RetrievalFilterMemberNotEquals{Value: attr}, nil
	case filter.OpLess:
		return &types.RetrievalFilterMemberLessThan{Value: attr}, nil
	case filter.OpLessEqual:
		return &types.RetrievalFilterMemberLessThanOrEquals{Value: attr}, nil
	case filter.OpGreater:
		return &types.RetrievalFilterMemberGreaterThan{Value: attr}, nil
	case filter.OpGreaterEqual:
		return &types.RetrievalFilterMemberGreaterThanOrEquals{Value: attr}, nil
	default:
		return nil, fmt.Errorf("bedrockkb: unexpected comparison operator '%s'", expr.Operator().String())
	}
}

func (v *visitor) convertIn(expr *filter.BinaryExpr) (types.RetrievalFilter, error) {
	attribute, err := listAttribute(expr)
	if err != nil {
		return nil, err
	}
	return &types.RetrievalFilterMemberIn{Value: attribute}, nil
}

func (v *visitor) convertHas(expr *filter.BinaryExpr) (types.RetrievalFilter, error) {
	key, err := keyName(expr.Left())
	if err != nil {
		return nil, err
	}
	value, err := extractLiteralValue(expr.Right())
	if err != nil {
		return nil, err
	}
	return &types.RetrievalFilterMemberListContains{Value: types.FilterAttribute{
		Key:   &key,
		Value: document.NewLazyDocument(value),
	}}, nil
}

func (v *visitor) convertNotIn(expr *filter.BinaryExpr) (types.RetrievalFilter, error) {
	attribute, err := listAttribute(expr)
	if err != nil {
		return nil, err
	}
	return &types.RetrievalFilterMemberNotIn{Value: attribute}, nil
}

// convertLike maps LIKE onto Bedrock's StringContains / StartsWith
// depending on the pattern shape.
func (v *visitor) convertLike(expr *filter.BinaryExpr) (types.RetrievalFilter, error) {
	key, err := keyName(expr.Left())
	if err != nil {
		return nil, err
	}
	value, err := extractLiteralValue(expr.Right())
	if err != nil {
		return nil, err
	}
	pattern, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("bedrockkb: LIKE requires a string pattern, got %T", value)
	}

	if strings.ContainsRune(pattern, '_') {
		return nil, errors.New("bedrockkb: LIKE '_' wildcard is not supported by Bedrock filters")
	}
	leadingWildcard := strings.HasPrefix(pattern, "%")
	trailingWildcard := strings.HasSuffix(pattern, "%")
	core := strings.TrimSuffix(strings.TrimPrefix(pattern, "%"), "%")
	if core == "" || strings.ContainsRune(core, '%') {
		return nil, fmt.Errorf("bedrockkb: LIKE pattern %q cannot be represented exactly by Bedrock filters", pattern)
	}
	if leadingWildcard && !trailingWildcard {
		return nil, fmt.Errorf("bedrockkb: LIKE suffix pattern %q is not supported by Bedrock filters", pattern)
	}

	attr := types.FilterAttribute{
		Key:   &key,
		Value: document.NewLazyDocument(core),
	}
	switch {
	case !leadingWildcard && trailingWildcard:
		// "foo%" → StartsWith
		return &types.RetrievalFilterMemberStartsWith{Value: attr}, nil
	case leadingWildcard && trailingWildcard:
		// "%foo%" → StringContains
		return &types.RetrievalFilterMemberStringContains{Value: attr}, nil
	default:
		// SQL LIKE without wildcards is exact equality.
		return &types.RetrievalFilterMemberEquals{Value: attr}, nil
	}
}

// invertBinary returns the boolean inverse of a single comparison —
// EQ↔NE, LT↔GE, and LE↔GT.
func invertBinary(expr *filter.BinaryExpr) (*filter.BinaryExpr, error) {
	inverted, err := expr.Inverse()
	if err != nil {
		return nil, fmt.Errorf("bedrockkb: cannot invert operator '%s': %w", expr.Operator(), err)
	}
	return inverted, nil
}

func listAttribute(expr *filter.BinaryExpr) (types.FilterAttribute, error) {
	key, err := keyName(expr.Left())
	if err != nil {
		return types.FilterAttribute{}, err
	}
	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return types.FilterAttribute{}, errors.New("bedrockkb: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return types.FilterAttribute{}, errors.New("bedrockkb: 'IN' requires a non-empty list")
	}
	values := make([]any, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := literalToValue(lit)
		if err != nil {
			return types.FilterAttribute{}, err
		}
		values = append(values, val)
	}
	return types.FilterAttribute{Key: &key, Value: document.NewLazyDocument(values)}, nil
}

func keyName(expr filter.Expr) (string, error) {
	switch node := expr.(type) {
	case *filter.Ident:
		return node.Name(), nil
	case *filter.IndexExpr:
		return "", errors.New("bedrockkb: nested metadata paths are not supported")
	default:
		return "", fmt.Errorf("unsupported left operand %T", node)
	}
}

func extractLiteralValue(expr filter.Expr) (any, error) {
	lit, ok := expr.(*filter.Literal)
	if !ok {
		return nil, fmt.Errorf("expected literal, got %T", expr)
	}
	return literalToValue(lit)
}

func literalToValue(lit *filter.Literal) (any, error) {
	return lit.Value()
}
