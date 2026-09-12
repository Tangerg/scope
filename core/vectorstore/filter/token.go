package filter

type tokenKind uint8

const (
	tokenInvalid tokenKind = iota
	tokenEOF
	tokenIdent
	tokenNumber
	tokenString
	tokenTrue
	tokenFalse
	tokenEqual
	tokenNotEqual
	tokenLess
	tokenLessEqual
	tokenGreater
	tokenGreaterEqual
	tokenAnd
	tokenOr
	tokenNot
	tokenIn
	tokenHas
	tokenLike
	tokenIs
	tokenNull
	tokenLeftParen
	tokenRightParen
	tokenLeftBracket
	tokenRightBracket
	tokenComma
)

type lexeme struct {
	kind       tokenKind
	literal    string
	start, end Position
}

func (t tokenKind) string() string {
	switch t {
	case tokenEOF:
		return "end of input"
	case tokenIdent:
		return "identifier"
	case tokenNumber:
		return "number"
	case tokenString:
		return "string"
	case tokenTrue, tokenFalse:
		return "boolean"
	case tokenEqual:
		return "=="
	case tokenNotEqual:
		return "!="
	case tokenLess:
		return "<"
	case tokenLessEqual:
		return "<="
	case tokenGreater:
		return ">"
	case tokenGreaterEqual:
		return ">="
	case tokenAnd:
		return "AND"
	case tokenOr:
		return "OR"
	case tokenNot:
		return "NOT"
	case tokenIn:
		return "IN"
	case tokenHas:
		return "HAS"
	case tokenLike:
		return "LIKE"
	case tokenIs:
		return "IS"
	case tokenNull:
		return "NULL"
	case tokenLeftParen:
		return "("
	case tokenRightParen:
		return ")"
	case tokenLeftBracket:
		return "["
	case tokenRightBracket:
		return "]"
	case tokenComma:
		return ","
	default:
		return "invalid token"
	}
}

func (t tokenKind) operator() Operator {
	switch t {
	case tokenEqual:
		return OpEqual
	case tokenNotEqual:
		return OpNotEqual
	case tokenLess:
		return OpLess
	case tokenLessEqual:
		return OpLessEqual
	case tokenGreater:
		return OpGreater
	case tokenGreaterEqual:
		return OpGreaterEqual
	default:
		return ""
	}
}

func (t tokenKind) literalKind() (LiteralKind, bool) {
	switch t {
	case tokenString:
		return LiteralString, true
	case tokenNumber:
		return LiteralNumber, true
	case tokenTrue, tokenFalse:
		return LiteralBool, true
	default:
		return "", false
	}
}
