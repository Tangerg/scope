// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parser

import (
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	xrefFieldCount    = 3
	maxXrefFieldWidth = 8
)

type xrefKind uint8

const (
	xrefAbsent xrefKind = iota
	xrefNull
	xrefUncompressed
	xrefCompressed
)

const (
	xrefStreamFree         = 0
	xrefStreamUncompressed = 1
	xrefStreamCompressed   = 2
)

type xref struct {
	kind   xrefKind
	ptr    objptr
	stream objptr
	offset int64
}

// Revisions are read newest first. A null entry must retain ownership so an
// older definition cannot resurrect a deleted or unknown object.
func (x *xref) admit(entry xref) {
	if x.kind == xrefAbsent {
		*x = entry
	}
}

func (x xref) resolves(ptr objptr) bool {
	return (x.kind == xrefUncompressed || x.kind == xrefCompressed) && x.ptr == ptr
}

type xrefLoader struct {
	reader     *Reader
	table      []xref
	visited    map[int64]bool
	newestSize int64
}

// Each revision owns its table entries before its supplemental stream; both
// precede Prev. One traversal follows Prev across table and stream revisions.
func (x *xrefLoader) load(start int64) ([]xref, objptr, dict, error) {
	var trailer dict
	var trailerPointer objptr
	for offset := object(start); offset != nil; {
		input, err := x.sectionBuffer(offset)
		if err != nil {
			return nil, objptr{}, nil, err
		}
		position := input.readOffset()
		if x.visited[position] {
			return nil, objptr{}, nil, fmt.Errorf("malformed PDF: cyclic xref Prev offset %d", position)
		}
		x.visited[position] = true
		var current dict
		var pointer objptr
		token := input.readToken()
		if token == keyword("xref") {
			x.table, err = readXrefTableData(input, x.table)
			if err != nil {
				return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %w", err)
			}
			var ok bool
			current, ok = input.readObject().(dict)
			if !ok {
				return nil, objptr{}, nil, errors.New("malformed PDF: xref table not followed by trailer dictionary")
			}
			if _, err = x.sectionSize(current); err != nil {
				return nil, objptr{}, nil, err
			}
			if supplemental := current["XRefStm"]; supplemental != nil {
				streamInput, streamErr := x.sectionBuffer(supplemental)
				if streamErr != nil {
					return nil, objptr{}, nil, streamErr
				}
				// A hybrid stream supplements this table; its Prev is ignored
				// by ISO 32000-1, Table 17. The table owns revision traversal.
				if _, _, streamErr = x.readStream(streamInput); streamErr != nil {
					return nil, objptr{}, nil, streamErr
				}
			}
		} else {
			input.unreadToken(token)
			pointer, current, err = x.readStream(input)
			if err != nil {
				return nil, objptr{}, nil, err
			}
		}
		if trailer == nil {
			trailer, trailerPointer = current, pointer
		}
		offset = current["Prev"]
	}
	if int64(len(x.table)) > x.newestSize {
		x.table = x.table[:x.newestSize]
	}
	return x.table, trailerPointer, trailer, nil
}

func (x *xrefLoader) sectionBuffer(offset object) (*buffer, error) {
	x.reader.budget.step(1)
	position, ok := offset.(int64)
	if !ok {
		return nil, fmt.Errorf("malformed PDF: xref offset is not an integer: %v", offset)
	}
	if position < 0 || position >= x.reader.end {
		return nil, fmt.Errorf("malformed PDF: invalid xref offset %d", position)
	}
	return newBuffer(io.NewSectionReader(x.reader.f, position, x.reader.end-position), position, x.reader.budget), nil
}

func (x *xrefLoader) sectionSize(header dict) (int64, error) {
	size, ok := header["Size"].(int64)
	if !ok || size < 0 {
		return 0, errors.New("malformed PDF: missing or invalid xref Size")
	}
	if size > x.reader.budget.limits.MaxObjects {
		panic(ErrBudget)
	}
	if x.newestSize < 0 {
		x.newestSize = size
	}
	if size > x.newestSize {
		return 0, errors.New("malformed PDF: xref section larger than latest section")
	}
	return size, nil
}

func (x *xrefLoader) readStream(input *buffer) (objptr, dict, error) {
	definition, ok := input.readObject().(objdef)
	if !ok {
		return objptr{}, nil, errors.New("malformed PDF: cross-reference stream not found")
	}
	body, ok := definition.obj.(stream)
	if !ok || body.hdr["Type"] != name("XRef") {
		return objptr{}, nil, errors.New("malformed PDF: cross-reference stream must have type XRef")
	}
	size, err := x.sectionSize(body.hdr)
	if err != nil {
		return objptr{}, nil, err
	}
	if size > 0 {
		x.table = ensureXrefLen(x.reader.budget, x.table, int(size-1))
	}
	x.table, err = readXrefStreamData(x.reader, body, x.table, size)
	if err != nil {
		return objptr{}, nil, fmt.Errorf("malformed PDF: read xref stream: %w", err)
	}
	return definition.ptr, body.hdr, nil
}

func readXrefStreamData(r *Reader, strm stream, table []xref, size int64) ([]xref, error) {
	index, _ := strm.hdr["Index"].(array)
	if index == nil {
		index = array{int64(0), size}
	}
	if len(index)%2 != 0 {
		return nil, errors.New("malformed PDF: invalid Index array")
	}
	ww, ok := strm.hdr["W"].(array)
	if !ok {
		return nil, fmt.Errorf("xref stream missing W array")
	}

	var w []int
	for _, x := range ww {
		i, ok := x.(int64)
		if !ok || i < 0 || i > maxXrefFieldWidth {
			return nil, errors.New("malformed PDF: invalid W array")
		}
		w = append(w, int(i))
	}
	if len(w) != xrefFieldCount {
		return nil, errors.New("malformed PDF: invalid W array")
	}

	v := Value{r, objptr{}, strm}
	wtotal := 0
	for _, wid := range w {
		wtotal += wid
	}
	buf := make([]byte, wtotal)
	data := v.Reader()
	defer data.Close()
	for len(index) > 0 {
		start, ok1 := index[0].(int64)
		n, ok2 := index[1].(int64)
		if !ok1 || !ok2 || start < 0 || n < 0 || start > size || n > size-start {
			return nil, errors.New("malformed PDF: invalid Index pair")
		}
		index = index[2:]
		for i := 0; i < int(n); i++ {
			r.budget.step(1)
			_, err := io.ReadFull(data, buf)
			if err != nil {
				return nil, fmt.Errorf("error reading xref stream: %w", err)
			}
			v1 := decodeUint(buf[0:w[0]])
			if w[0] == 0 {
				v1 = xrefStreamUncompressed
			}
			v2 := decodeUint(buf[w[0] : w[0]+w[1]])
			v3 := decodeUint(buf[w[0]+w[1] : w[0]+w[1]+w[2]])
			x := int(start) + i
			table = ensureXrefLen(r.budget, table, x)
			switch v1 {
			case xrefStreamUncompressed:
				if v2 > math.MaxInt64 || v3 > math.MaxUint16 {
					return nil, errors.New("malformed PDF: invalid uncompressed xref entry")
				}
				table[x].admit(xref{kind: xrefUncompressed, ptr: objptr{uint32(x), uint16(v3)}, offset: int64(v2)})
			case xrefStreamCompressed:
				if v2 > math.MaxUint32 || v3 > math.MaxInt64 {
					return nil, errors.New("malformed PDF: invalid compressed xref entry")
				}
				table[x].admit(xref{kind: xrefCompressed, ptr: objptr{uint32(x), 0}, stream: objptr{uint32(v2), 0}, offset: int64(v3)})
			case xrefStreamFree:
				table[x].admit(xref{kind: xrefNull})
			default:
				table[x].admit(xref{kind: xrefNull})
			}
		}
	}
	return table, nil
}

func decodeUint(b []byte) uint64 {
	var x uint64
	for _, c := range b {
		x = x<<8 | uint64(c)
	}
	return x
}

func ensureXrefLen(grant *budget, table []xref, x int) []xref {
	if x < 0 {
		panic(fmt.Errorf("malformed PDF: negative object index"))
	}
	if x < len(table) {
		return table
	}
	grant.objects(int64(x - len(table) + 1))
	return append(table, make([]xref, x-len(table)+1)...)
}

func readXrefTableData(b *buffer, table []xref) ([]xref, error) {
	for {
		tok := b.readToken()
		if tok == keyword("trailer") {
			break
		}
		start, ok1 := tok.(int64)
		n, ok2 := b.readToken().(int64)
		if !ok1 || !ok2 || start < 0 || n < 0 || start > b.budget.limits.MaxObjects || n > b.budget.limits.MaxObjects-start {
			return nil, fmt.Errorf("malformed xref table")
		}
		for i := 0; i < int(n); i++ {
			off, ok1 := b.readToken().(int64)
			gen, ok2 := b.readToken().(int64)
			alloc, ok3 := b.readToken().(keyword)
			if !ok1 || !ok2 || !ok3 || off < 0 || gen < 0 || gen > math.MaxUint16 || alloc != keyword("f") && alloc != keyword("n") {
				return nil, fmt.Errorf("malformed xref table")
			}
			x := int(start) + i
			table = ensureXrefLen(b.budget, table, x)
			if alloc == "n" {
				table[x].admit(xref{kind: xrefUncompressed, ptr: objptr{uint32(x), uint16(gen)}, offset: off})
			} else {
				table[x].admit(xref{kind: xrefNull})
			}
		}
	}
	return table, nil
}
