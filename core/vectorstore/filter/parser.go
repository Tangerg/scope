package filter

import (
	"fmt"
	"strings"
)

type parser struct {
	scanner *scanner
	current lexeme
}

func newParser(input string) (*parser, error) {
	if strings.TrimSpace(input) == "" {
		return nil, newSyntaxError(Position{Line: 1, Column: 1}, "", "expression is empty")
	}
	p := &parser{scanner: newScanner(input)}
	if err := p.advance(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *parser) parse() (Predicate, error) {
	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.current.kind != tokenEOF {
		return nil, p.unexpected("end of expression")
	}
	return expr, nil
}

func (p *parser) parseOr() (Predicate, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.current.kind == tokenOr {
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{
			left: left, operator: OpOr, right: right,
			start: left.Start(), end: right.End(),
		}
	}
	return left, nil
}

func (p *parser) parseAnd() (Predicate, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.current.kind == tokenAnd {
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{
			left: left, operator: OpAnd, right: right,
			start: left.Start(), end: right.End(),
		}
	}
	return left, nil
}

func (p *parser) parseNot() (Predicate, error) {
	if p.current.kind == tokenNot {
		op := p.current
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{
			operator: OpNot, right: right,
			start: op.start, end: right.End(),
		}, nil
	}

	if p.current.kind == tokenLeftParen {
		if err := p.advance(); err != nil {
			return nil, err
		}
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokenRightParen, "closing ')'"); err != nil {
			return nil, err
		}
		return expr, nil
	}
	return p.parsePredicate()
}

func (p *parser) parsePredicate() (Predicate, error) {
	left, err := p.parseSelector()
	if err != nil {
		return nil, err
	}
	switch p.current.kind {
	case tokenNot:
		return p.parseNotIn(left)
	case tokenEqual, tokenNotEqual, tokenLess, tokenLessEqual, tokenGreater, tokenGreaterEqual:
		return p.parseComparison(left)
	case tokenIn:
		return p.parseIn(left)
	case tokenHas:
		return p.parseLiteralPredicate(left, OpHas)
	case tokenLike:
		return p.parseLiteralPredicate(left, OpLike)
	case tokenIs:
		return p.parseNullPredicate(left)
	default:
		return nil, p.unexpected("comparison, IN, HAS, LIKE, or IS")
	}
}

func (p *parser) parseNotIn(left Selector) (Predicate, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}
	if _, err := p.expect(tokenIn, "IN after NOT"); err != nil {
		return nil, err
	}
	membership, err := p.parseInList(left)
	if err != nil {
		return nil, err
	}
	return &UnaryExpr{
		operator: OpNot, right: membership,
		start: left.Start(), end: membership.End(),
	}, nil
}

func (p *parser) parseComparison(left Selector) (Predicate, error) {
	operator := p.current.kind.operator()
	return p.parseLiteralPredicate(left, operator)
}

func (p *parser) parseLiteralPredicate(left Selector, operator Operator) (Predicate, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}
	right, err := p.parseLiteral()
	if err != nil {
		return nil, err
	}
	return binaryPredicate(left, operator, right), nil
}

func (p *parser) parseIn(left Selector) (Predicate, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}
	return p.parseInList(left)
}

func (p *parser) parseInList(left Selector) (*BinaryExpr, error) {
	list, err := p.parseList()
	if err != nil {
		return nil, err
	}
	return binaryPredicate(left, OpIn, list), nil
}

func (p *parser) parseNullPredicate(left Selector) (Predicate, error) {
	if err := p.advance(); err != nil {
		return nil, err
	}
	negated := p.current.kind == tokenNot
	if negated {
		if err := p.advance(); err != nil {
			return nil, err
		}
	}
	nullToken, err := p.expect(tokenNull, "NULL after IS")
	if err != nil {
		return nil, err
	}
	null := &Literal{
		kind: LiteralNull, text: string(LiteralNull),
		start: nullToken.start, end: nullToken.end,
	}
	test := binaryPredicate(left, OpIs, null)
	if !negated {
		return test, nil
	}
	return &UnaryExpr{
		operator: OpNot, right: test,
		start: left.Start(), end: null.End(),
	}, nil
}

func binaryPredicate(left Expr, operator Operator, right Expr) *BinaryExpr {
	return &BinaryExpr{
		left: left, operator: operator, right: right,
		start: left.Start(), end: right.End(),
	}
}

func (p *parser) parseSelector() (Selector, error) {
	identToken, err := p.expect(tokenIdent, "identifier")
	if err != nil {
		return nil, err
	}

	var left Selector = &Ident{
		name:  identToken.literal,
		start: identToken.start, end: identToken.end,
	}
	for p.current.kind == tokenLeftBracket {
		if err := p.advance(); err != nil {
			return nil, err
		}
		index, err := p.parseLiteral()
		if err != nil {
			return nil, err
		}
		if !index.IsString() && !index.IsNumber() {
			return nil, newSyntaxError(index.Start(), index.text, "index must be a string or number")
		}
		closing, err := p.expect(tokenRightBracket, "closing ']'")
		if err != nil {
			return nil, err
		}
		left = &IndexExpr{
			left: left, index: index,
			start: left.Start(), end: closing.end,
		}
	}
	return left, nil
}

func (p *parser) parseList() (*ListLiteral, error) {
	opening, err := p.expect(tokenLeftParen, "opening '('")
	if err != nil {
		return nil, err
	}
	if p.current.kind == tokenRightParen {
		return nil, newSyntaxError(p.current.start, p.current.literal, "list cannot be empty")
	}

	first, err := p.parseLiteral()
	if err != nil {
		return nil, err
	}
	values := []*Literal{first}
	for p.current.kind == tokenComma {
		if advanceErr := p.advance(); advanceErr != nil {
			return nil, advanceErr
		}
		if p.current.kind == tokenRightParen {
			return nil, newSyntaxError(p.current.start, p.current.literal, "trailing comma in list")
		}
		value, parseLiteralErr := p.parseLiteral()
		if parseLiteralErr != nil {
			return nil, parseLiteralErr
		}
		values = append(values, value)
	}

	closing, err := p.expect(tokenRightParen, "closing ')'")
	if err != nil {
		return nil, err
	}
	return &ListLiteral{
		values: values,
		start:  opening.start, end: closing.end,
	}, nil
}

func (p *parser) parseLiteral() (*Literal, error) {
	kind, ok := p.current.kind.literalKind()
	if !ok {
		return nil, p.unexpected("string, number, or boolean literal")
	}
	current := p.current
	if err := p.advance(); err != nil {
		return nil, err
	}
	return &Literal{
		kind: kind, text: current.literal,
		start: current.start, end: current.end,
	}, nil
}

func (p *parser) expect(kind tokenKind, expected string) (lexeme, error) {
	if p.current.kind != kind {
		return lexeme{}, p.unexpected(expected)
	}
	current := p.current
	if err := p.advance(); err != nil {
		return lexeme{}, err
	}
	return current, nil
}

func (p *parser) advance() error {
	next, err := p.scanner.next()
	if err != nil {
		return err
	}
	p.current = next
	return nil
}

func (p *parser) unexpected(expected string) error {
	literal := p.current.literal
	if p.current.kind == tokenEOF {
		literal = tokenEOF.string()
	}
	return newSyntaxError(
		p.current.start,
		literal,
		fmt.Sprintf("expected %s, got %s", expected, literal),
	)
}
