// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parser

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

type Page struct {
	V Value
}

func (p Page) findInherited(key string) Value {
	for v := p.V; !v.IsNull(); v = v.Key("Parent") {
		if r := v.Key(key); !r.IsNull() {
			return r
		}
	}
	return Value{}
}

func (p Page) Resources() Value {
	return p.findInherited("Resources")
}

func (p Page) Fonts() []string {
	return p.Resources().Key("Font").Keys()
}

func (p Page) Font(name string) Font {
	return Font{p.Resources().Key("Font").Key(name), nil}
}

// fontCache returns the page's fonts keyed by name, parsing each font only
// once so that repeated text operations don't re-parse its charmap.
func (p Page) fontCache() map[string]*Font {
	fonts := make(map[string]*Font)
	for _, name := range p.Fonts() {
		if _, ok := fonts[name]; !ok {
			f := p.Font(name)
			fonts[name] = &f
		}
	}
	return fonts
}

type Font struct {
	V   Value
	enc TextEncoding
}

// Encoder returns the encoding between font code point sequences and UTF-8.
//
// The receiver is a pointer so the parsed encoding is cached on the Font;
// with a value receiver the assignment to f.enc would be discarded with the
// copy, defeating the caching entirely.
func (f *Font) Encoder() TextEncoding {
	if f.enc == nil {
		f.enc = f.getEncoder()
	}
	return f.enc
}

func (f Font) getEncoder() TextEncoding {
	enc := f.V.Key("Encoding")
	switch enc.Kind() {
	case Name:
		switch enc.Name() {
		case "WinAnsiEncoding":
			return &byteEncoder{&winAnsiEncoding}
		case "MacRomanEncoding":
			return &byteEncoder{&macRomanEncoding}
		case "Identity-H":
			return f.charmapEncoding()
		case "UniGB-UCS2-H":
			return &ucs2Encoder{}
		default:

			return &nopEncoder{}
		}
	case Dict:
		return &dictEncoder{enc.Key("Differences")}
	case Null:
		return f.charmapEncoding()
	default:

		return &nopEncoder{}
	}
}

func (f *Font) charmapEncoding() TextEncoding {
	toUnicode := f.V.Key("ToUnicode")
	if toUnicode.Kind() == Stream {
		m := readCmap(toUnicode)
		if m == nil {
			return &nopEncoder{}
		}
		return m
	}

	return &byteEncoder{&pdfDocEncoding}
}

type dictEncoder struct {
	v Value
}

func (d *dictEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		ch := rune(raw[i])
		n := -1
		for j := 0; j < d.v.Len(); j++ {
			d.v.r.budget.step(1)
			x := d.v.Index(j)
			if x.Kind() == Integer {
				n = int(x.Int64())
				continue
			}
			if x.Kind() == Name {
				if int(raw[i]) == n {
					r := nameToRune[x.Name()]
					if r != 0 {
						ch = r
						break
					}
				}
				n++
			}
		}
		r = append(r, ch)
	}
	return string(r)
}

// A TextEncoding represents a mapping between
// font code points and UTF-8 text.
type TextEncoding interface {
	Decode(raw string) (text string)
}

type nopEncoder struct {
}

func (n *nopEncoder) Decode(raw string) (text string) {
	return raw
}

type ucs2Encoder struct{}

func (u *ucs2Encoder) Decode(raw string) (text string) {
	return utf16Decode(raw)
}

type byteEncoder struct {
	table *[256]rune
}

func (b *byteEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		r = append(r, b.table[raw[i]])
	}
	return string(r)
}

type byteRange struct {
	low  string
	high string
}

type bfchar struct {
	orig string
	repl string
}

type bfrange struct {
	lo  string
	hi  string
	dst Value
}

type cmap struct {
	budget  *budget
	space   [4][]byteRange
	bfrange []bfrange
	bfchar  []bfchar
}

func (c *cmap) Decode(raw string) (text string) {
	var r []rune
Parse:
	for len(raw) > 0 {
		c.budget.step(1)
		for n := 1; n <= len(c.space) && n <= len(raw); n++ {
			for _, space := range c.space[n-1] {
				c.budget.step(1)
				if space.low <= raw[:n] && raw[:n] <= space.high {
					text := raw[:n]
					raw = raw[n:]
					for _, bfchar := range c.bfchar {
						c.budget.step(1)
						if len(bfchar.orig) == n && bfchar.orig == text {
							c.budget.expand(int64(len(bfchar.repl)) * 4)
							r = append(r, []rune(utf16Decode(bfchar.repl))...)
							continue Parse
						}
					}
					for _, bfrange := range c.bfrange {
						c.budget.step(1)
						if len(bfrange.lo) == n && bfrange.lo <= text && text <= bfrange.hi {
							if bfrange.dst.Kind() == String {
								s := bfrange.dst.RawString()
								if bfrange.lo != text {
									s = addOffset(s, beUint(text)-beUint(bfrange.lo))
								}
								c.budget.expand(int64(len(s)) * 4)
								r = append(r, []rune(utf16Decode(s))...)
								continue Parse
							}
							if bfrange.dst.Kind() == Array {
								offset := beUint(text) - beUint(bfrange.lo)
								v := bfrange.dst.Index(int(offset))
								if v.Kind() == String {
									s := v.RawString()
									c.budget.expand(int64(len(s)) * 4)
									r = append(r, []rune(utf16Decode(s))...)
									continue Parse
								}

							}
							r = append(r, noRune)
							continue Parse
						}
					}
					r = append(r, noRune)
					continue Parse
				}
			}
		}

		r = append(r, noRune)
		raw = raw[1:]
	}
	return string(r)
}

func readCmap(toUnicode Value) *cmap {
	n := -1
	m := cmap{budget: toUnicode.r.budget}
	ok := true
	Interpret(toUnicode, func(stk *Stack, op string) {
		if !ok {
			return
		}
		switch op {
		case "findresource":
			stk.Pop()
			stk.Pop()
			stk.Push(newDict())
		case "begincmap":
			stk.Push(newDict())
		case "endcmap":
			stk.Pop()
		case "begincodespacerange":
			n = int(stk.Pop().Int64())
			m.budget.step(int64(n))
		case "endcodespacerange":
			if n < 0 {

				ok = false
				return
			}
			for i := 0; i < n; i++ {
				m.budget.objects(1)
				hi, lo := stk.Pop().RawString(), stk.Pop().RawString()
				if len(lo) == 0 || len(lo) > len(m.space) || len(lo) != len(hi) {

					ok = false
					return
				}
				m.space[len(lo)-1] = append(m.space[len(lo)-1], byteRange{lo, hi})
			}
			n = -1
		case "beginbfchar":
			n = int(stk.Pop().Int64())
			m.budget.step(int64(n))
		case "endbfchar":
			if n < 0 {
				panic("missing beginbfchar")
			}
			for i := 0; i < n; i++ {
				m.budget.objects(1)
				repl, orig := stk.Pop().RawString(), stk.Pop().RawString()
				m.bfchar = append(m.bfchar, bfchar{orig, repl})
			}
		case "beginbfrange":
			n = int(stk.Pop().Int64())
			m.budget.step(int64(n))
		case "endbfrange":
			if n < 0 {
				panic("missing beginbfrange")
			}
			for i := 0; i < n; i++ {
				m.budget.objects(1)
				dst, srcHi, srcLo := stk.Pop(), stk.Pop().RawString(), stk.Pop().RawString()
				m.bfrange = append(m.bfrange, bfrange{srcLo, srcHi, dst})
			}
		case "defineresource":
			stk.Pop().Name()
			value := stk.Pop()
			stk.Pop().Name()
			stk.Push(value)
		}
	})
	if !ok {
		return nil
	}
	return &m
}

func popArgs(stk *Stack) []Value {
	n := stk.Len()
	args := make([]Value, n)
	for i := n - 1; i >= 0; i-- {
		args[i] = stk.Pop()
	}
	return args
}

func recoverTo(err *error, reset func()) {
	if r := recover(); r != nil {
		reset()
		if cause, ok := r.(error); ok {
			*err = cause
		} else {
			*err = errors.New(fmt.Sprint(r))
		}
	}
}

func decodeText(enc TextEncoding, raw string) string {
	var b strings.Builder
	for _, ch := range enc.Decode(raw) {
		b.WriteRune(ch)
	}
	return b.String()
}

func (p Page) PlainText() (result string, err error) {
	defer recoverTo(&err, func() { result = "" })

	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return "", nil
	}
	strm := p.V.Key("Contents")
	var enc TextEncoding = &nopEncoder{}

	fonts := p.fontCache()

	var textBuilder bytes.Buffer
	showText := func(s string) {
		p.V.r.budget.expand(int64(len(s)))
		textBuilder.WriteString(s)
	}
	showEncodedText := func(s string) {
		p.V.r.budget.expand(int64(len(s)) * 4)
		showText(decodeText(enc, s))
	}

	Interpret(strm, func(stk *Stack, op string) {
		args := popArgs(stk)

		switch op {
		case "BT": // add a space between text objects
			showText("\n")
		case "T*":
			showText("\n")
		case "Tf": // set text font and size
			if len(args) != 2 {
				panic("invalid Tf operands")
			}
			if font, ok := fonts[args[0].Name()]; ok {
				enc = font.Encoder()
			} else {
				enc = &nopEncoder{}
			}
		case "\"": // set spacing, move to next line, and show text
			if len(args) != 3 {
				panic("bad \" operator")
			}
			showText("\n")
			showEncodedText(args[2].RawString())
		case "'", "Tj":
			if len(args) != 1 {
				panic("bad Tj operator")
			}
			if op == "'" {
				showText("\n")
			}
			showEncodedText(args[0].RawString())
		case "TJ":
			if len(args) != 1 || args[0].Kind() != Array {
				panic("invalid TJ operands")
			}
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == String {
					showEncodedText(x.RawString())
				}
			}
		}
	})
	return textBuilder.String(), nil
}

func beUint(s string) uint32 {
	var v uint32
	for i := 0; i < len(s); i++ {
		v = v<<8 | uint32(s[i])
	}
	return v
}

func addOffset(s string, off uint32) string {
	b := []byte(s)
	carry := off
	for i := len(b) - 1; i >= 0 && carry > 0; i-- {
		sum := uint32(b[i]) + carry&0xff
		b[i] = byte(sum)
		carry = carry>>8 + sum>>8
	}
	return string(b)
}
