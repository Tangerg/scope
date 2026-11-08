// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parser

import (
	"fmt"
	"unicode/utf8"
)

const maximumCharacterCodeBytes = 4

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
	space   [maximumCharacterCodeBytes][]byteRange
	bfrange []bfrange
	bfchar  []bfchar
}

func (c *cmap) Decode(raw string) string {
	var runes []rune
	for len(raw) > 0 {
		c.budget.step(1)
		code, width := c.nextCode(raw)
		raw = raw[width:]
		if code == "" {
			runes = append(runes, noRune)
			continue
		}
		mapped, found := c.lookup(code)
		if !found {
			runes = append(runes, noRune)
			continue
		}
		c.budget.expand(int64(len(mapped)) * utf8.UTFMax)
		runes = append(runes, []rune(utf16Decode(mapped))...)
	}
	return string(runes)
}

func (c *cmap) nextCode(raw string) (string, int) {
	for width := 1; width <= len(c.space) && width <= len(raw); width++ {
		for _, interval := range c.space[width-1] {
			c.budget.step(1)
			if interval.low <= raw[:width] && raw[:width] <= interval.high {
				return raw[:width], width
			}
		}
	}
	return "", 1
}

func (c *cmap) lookup(code string) (string, bool) {
	for _, mapping := range c.bfchar {
		c.budget.step(1)
		if mapping.orig == code {
			return mapping.repl, true
		}
	}
	for _, mapping := range c.bfrange {
		c.budget.step(1)
		if len(mapping.lo) != len(code) || code < mapping.lo || code > mapping.hi {
			continue
		}
		offset := beUint(code) - beUint(mapping.lo)
		switch mapping.dst.Kind() {
		case String:
			mapped := mapping.dst.RawString()
			if offset != 0 {
				mapped = addOffset(mapped, offset)
			}
			return mapped, true
		case Array:
			mapped := mapping.dst.Index(int(offset))
			return mapped.RawString(), mapped.Kind() == String
		default:
			return "", false
		}
	}
	return "", false
}

func readCmap(toUnicode Value) *cmap {
	if !toUnicode.Key("UseCMap").IsNull() {
		panic(fmt.Errorf("%w: inherited ToUnicode CMap", ErrUnsupportedFontEncoding))
	}
	n := -1
	m := cmap{budget: toUnicode.r.budget}
	ok := true
	Interpret(toUnicode, Value{}, func(stk *Stack, op string) {
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
		default:
			panic(fmt.Errorf("%w: ToUnicode CMap operator %q", ErrUnsupportedFontEncoding, op))
		}
	})
	if !ok {
		return nil
	}
	return &m
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
