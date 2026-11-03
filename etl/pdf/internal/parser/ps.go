// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parser

type Stack struct {
	budget *budget
	stack  []Value
}

func (s *Stack) Len() int {
	return len(s.stack)
}

func (s *Stack) Push(v Value) {
	s.budget.objects(1)
	s.stack = append(s.stack, v)
}

func (s *Stack) Pop() Value {
	s.budget.step(1)
	n := len(s.stack)
	if n == 0 {
		return Value{}
	}
	v := s.stack[n-1]
	s.stack[n-1] = Value{}
	s.stack = s.stack[:n-1]
	return v
}

func newDict() Value {
	return Value{nil, objptr{}, make(dict)}
}

// Embedded CMaps need dictionary lookup but not executable PostScript blocks.
// Restricting interpretation to this subset keeps all work under the parse grant.
func Interpret(strm Value, do func(stk *Stack, op string)) {
	stk := Stack{budget: strm.r.budget}
	var dicts []dict
	rd := &contentStream{source: strm}
	defer rd.Close()

	b := newBuffer(rd, 0, strm.r.budget)
	b.allowEOF = true
	b.allowObjptr = false
	b.allowStream = false

Reading:
	for {
		tok := b.readToken()
		if isEOF(tok) {
			break
		}
		if kw, ok := tok.(keyword); ok {
			switch kw {
			case "null", "[", "]", "<<", ">>":
				break
			case "dict":
				stk.Pop()
				stk.Push(Value{nil, objptr{}, make(dict)})
				continue
			case "currentdict":
				if len(dicts) == 0 {
					panic("no current dictionary")
				}
				stk.Push(Value{nil, objptr{}, dicts[len(dicts)-1]})
				continue
			case "begin":
				d := stk.Pop()
				if d.Kind() != Dict {
					panic("cannot begin non-dict")
				}
				dicts = append(dicts, d.data.(dict))
				continue
			case "end":
				if len(dicts) <= 0 {
					panic("mismatched begin/end")
				}
				dicts = dicts[:len(dicts)-1]
				continue
			case "def":
				val := stk.Pop()
				if len(dicts) <= 0 {
					panic("definition outside dictionary")
				}
				key, ok := stk.Pop().data.(name)
				if !ok {
					panic("dictionary key is not a name")
				}
				dicts[len(dicts)-1][key] = val.data
				continue
			case "pop":
				stk.Pop()
				continue
			default:
				for i := len(dicts) - 1; i >= 0; i-- {
					b.budget.step(1)
					if v, ok := dicts[i][name(kw)]; ok {
						stk.Push(Value{nil, objptr{}, v})
						continue Reading
					}
				}
				do(&stk, string(kw))
				continue
			}
		}
		b.unreadToken(tok)
		obj := b.readObject()
		stk.Push(Value{nil, objptr{}, obj})
	}
}
