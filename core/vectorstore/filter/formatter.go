package filter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"
)

type formatter struct {
	output strings.Builder
}

func formatPredicate(predicate Predicate) string {
	formatted, err := new(formatter).format(predicate)
	if err != nil {
		return ""
	}
	return formatted
}

func (f *formatter) format(predicate Predicate) (string, error) {
	if err := f.expression(predicate, formatRoot, false); err != nil {
		return "", err
	}
	return f.output.String(), nil
}

type formatPrecedence uint8

const (
	formatRoot formatPrecedence = iota
	formatOr
	formatAnd
	formatTest
)

func (f *formatter) expression(expr Expr, parent formatPrecedence, right bool) error {
	if lo.IsNil(expr) {
		return errors.New("filter: expression is nil")
	}

	switch node := expr.(type) {
	case *Ident:
		f.output.WriteString(node.name)
	case *Literal:
		f.literal(node)
	case *ListLiteral:
		f.output.WriteByte('(')
		for i, value := range node.values {
			if i > 0 {
				f.output.WriteString(", ")
			}
			if err := f.expression(value, formatRoot, false); err != nil {
				return err
			}
		}
		f.output.WriteByte(')')
	case *IndexExpr:
		if err := f.expression(node.left, formatTest, false); err != nil {
			return err
		}
		f.output.WriteByte('[')
		if err := f.expression(node.index, formatRoot, false); err != nil {
			return err
		}
		f.output.WriteByte(']')
	case *UnaryExpr:
		f.output.WriteString(node.operator.String())
		f.output.WriteString(" (")
		if err := f.expression(node.right, formatRoot, false); err != nil {
			return err
		}
		f.output.WriteByte(')')
	case *BinaryExpr:
		return f.binary(node, parent, right)
	default:
		return fmt.Errorf("filter: unsupported expression %T", expr)
	}
	return nil
}

func (f *formatter) binary(binary *BinaryExpr, parent formatPrecedence, right bool) error {
	precedence := f.precedence(binary)
	wrapped := precedence < parent || right && precedence == parent && precedence != formatTest
	if wrapped {
		f.output.WriteByte('(')
	}
	if err := f.expression(binary.left, precedence, false); err != nil {
		return err
	}
	f.output.WriteByte(' ')
	f.output.WriteString(binary.operator.String())
	f.output.WriteByte(' ')
	if err := f.expression(binary.right, precedence, true); err != nil {
		return err
	}
	if wrapped {
		f.output.WriteByte(')')
	}
	return nil
}

func (f *formatter) precedence(binary *BinaryExpr) formatPrecedence {
	switch binary.operator {
	case OpOr:
		return formatOr
	case OpAnd:
		return formatAnd
	default:
		return formatTest
	}
}

var filterStringEscaper = strings.NewReplacer(
	`\`, `\\`,
	`'`, `\'`,
	"\n", `\n`,
	"\t", `\t`,
	"\r", `\r`,
)

func (f *formatter) literal(literal *Literal) {
	if literal.IsString() {
		f.output.WriteByte('\'')
		f.output.WriteString(filterStringEscaper.Replace(literal.text))
		f.output.WriteByte('\'')
		return
	}
	f.output.WriteString(literal.text)
}
