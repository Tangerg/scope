package filter

import (
	"errors"
	"fmt"
	"unicode"
)

type Ident struct {
	name  string
	start Position
	end   Position
}

func (*Ident) expr()     {}
func (*Ident) selector() {}

func (i *Ident) Name() string {
	if i == nil {
		return ""
	}
	return i.name
}

func (i *Ident) Path() ([]string, error) {
	if err := i.validate(); err != nil {
		return nil, err
	}
	return []string{i.name}, nil
}

func (i *Ident) Start() Position {
	if i == nil {
		return Position{}
	}
	return i.start
}

func (i *Ident) End() Position {
	if i == nil {
		return Position{}
	}
	return i.end
}

func (i *Ident) Equal(other Expr) bool {
	o, ok := other.(*Ident)
	return ok && i != nil && o != nil && i.name == o.name
}

func (i *Ident) validate() error {
	if i == nil {
		return errors.New("filter: identifier is nil")
	}
	if !i.validName() {
		return fmt.Errorf("filter: invalid identifier %q at %s", i.name, i.Start())
	}
	return nil
}

func (i *Ident) validName() bool {
	if keywordKind(i.name) != tokenIdent {
		return false
	}
	first := true
	for _, r := range i.name {
		if first {
			if !unicode.IsLetter(r) {
				return false
			}
			first = false
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return !first
}
