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

var tokenKindNames = [...]string{
	tokenEOF:          "end of input",
	tokenIdent:        "identifier",
	tokenNumber:       "number",
	tokenString:       "string",
	tokenTrue:         "boolean",
	tokenFalse:        "boolean",
	tokenNull:         "NULL",
	tokenLeftParen:    "(",
	tokenRightParen:   ")",
	tokenLeftBracket:  "[",
	tokenRightBracket: "]",
	tokenComma:        ",",
}

func (t tokenKind) string() string {
	if operator := t.operator(); operator != "" {
		if operator.IsComparisonOperator() {
			return string(operator)
		}
		return operator.Name()
	}
	if int(t) < len(tokenKindNames) && tokenKindNames[t] != "" {
		return tokenKindNames[t]
	}
	return "invalid token"
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
	case tokenAnd:
		return OpAnd
	case tokenOr:
		return OpOr
	case tokenNot:
		return OpNot
	case tokenIn:
		return OpIn
	case tokenHas:
		return OpHas
	case tokenLike:
		return OpLike
	case tokenIs:
		return OpIs
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
