package filter

type IdentifierValue interface {
	string | *Ident
}

func NewIdent[T IdentifierValue](value T) *Ident {
	if ident, ok := any(value).(*Ident); ok {
		return ident
	}
	return &Ident{name: any(value).(string)}
}

func leftOperand[L IdentifierValue | *IndexExpr](left L) Selector {
	switch typed := any(left).(type) {
	case *IndexExpr:
		return typed
	case *Ident:
		return typed
	default:
		return &Ident{name: typed.(string)}
	}
}
