// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package parser keeps PDF decoding under a per-read resource grant. It is an
// internal fork; provenance and update constraints are recorded in README.md.
package parser

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rc4"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

type Reader struct {
	budget     *budget
	resolving  map[objptr]bool
	f          io.ReaderAt
	end        int64
	xref       []xref
	trailer    dict
	trailerptr objptr
	key        []byte
	useAES     bool
}

func NewReader(ctx context.Context, f io.ReaderAt, size int64, limits Limits, userPassword []byte) (r *Reader, err error) {
	defer func() {
		if x := recover(); x != nil {
			r = nil
			if e, ok := x.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("malformed PDF: %v", x)
			}
		}
	}()

	grant := newBudget(ctx, limits)
	f = &budgetReaderAt{source: f, budget: grant}

	buf := make([]byte, 10)
	if _, readErr := f.ReadAt(buf, 0); readErr != nil {
		return nil, readErr
	}
	if !bytes.HasPrefix(buf, []byte("%PDF-1.")) || buf[7] < '0' || buf[7] > '7' || buf[8] != '\r' && buf[8] != '\n' {
		return nil, fmt.Errorf("not a PDF file: invalid header")
	}
	end := size
	const endChunk = 100
	tailSize := min(int64(endChunk), size)
	buf = make([]byte, tailSize)
	if _, readErr := f.ReadAt(buf, end-tailSize); readErr != nil {
		return nil, readErr
	}
	for len(buf) > 0 && (buf[len(buf)-1] == '\n' || buf[len(buf)-1] == '\r') {
		buf = buf[:len(buf)-1]
	}
	buf = bytes.TrimRight(buf, "\r\n\t ")
	if !bytes.HasSuffix(buf, []byte("%%EOF")) {
		return nil, fmt.Errorf("not a PDF file: missing %%%%EOF")
	}
	i := findLastLine(buf, "startxref")
	if i < 0 {
		return nil, fmt.Errorf("malformed PDF file: missing final startxref")
	}

	r = &Reader{
		f: f, end: end, budget: grant, resolving: make(map[objptr]bool),
	}
	pos := end - tailSize + int64(i)
	b := newBuffer(io.NewSectionReader(f, pos, end-pos), pos, grant)
	if b.readToken() != keyword("startxref") {
		return nil, fmt.Errorf("malformed PDF file: missing startxref")
	}
	startxref, ok := b.readToken().(int64)
	if !ok {
		return nil, fmt.Errorf("malformed PDF file: startxref not followed by integer")
	}
	if startxref < 0 || startxref >= r.end {
		return nil, fmt.Errorf("malformed PDF: invalid startxref")
	}
	loader := xrefLoader{reader: r, visited: make(map[int64]bool), newestSize: -1}
	xref, trailerptr, trailer, err := loader.load(startxref)
	if err != nil {
		return nil, err
	}
	r.xref = xref
	r.trailer = trailer
	r.trailerptr = trailerptr
	if trailer["Encrypt"] == nil {
		return r, nil
	}
	if err = r.initEncrypt(userPassword); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reader) Trailer() Value {
	return Value{r, r.trailerptr, r.trailer}
}

func findLastLine(buf []byte, s string) int {
	bs := []byte(s)
	max := len(buf)
	for {
		i := bytes.LastIndex(buf[:max], bs)
		if i <= 0 || i+len(bs) >= len(buf) {
			return -1
		}
		if (buf[i-1] == '\n' || buf[i-1] == '\r') && (buf[i+len(bs)] == '\n' || buf[i+len(bs)] == '\r') {
			return i
		}
		max = i
	}
}

type Value struct {
	r    *Reader
	ptr  objptr
	data interface{}
}

func (v Value) IsNull() bool {
	return v.data == nil
}

type ValueKind int

const (
	Null ValueKind = iota
	Bool
	Integer
	Real
	String
	Name
	Dict
	Array
	Stream
)

func (v Value) Kind() ValueKind {
	switch v.data.(type) {
	default:
		return Null
	case bool:
		return Bool
	case int64:
		return Integer
	case float64:
		return Real
	case string:
		return String
	case name:
		return Name
	case dict:
		return Dict
	case array:
		return Array
	case stream:
		return Stream
	}
}

func (v Value) Int64() int64 {
	x, ok := v.data.(int64)
	if !ok {
		return 0
	}
	return x
}

func (v Value) RawString() string {
	x, ok := v.data.(string)
	if !ok {
		return ""
	}
	return x
}

func (v Value) Name() string {
	x, ok := v.data.(name)
	if !ok {
		return ""
	}
	return string(x)
}

func (v Value) Key(key string) Value {
	x, ok := v.data.(dict)
	if !ok {
		strm, ok := v.data.(stream)
		if !ok {
			return Value{}
		}
		x = strm.hdr
	}
	return v.r.resolve(v.ptr, x[name(key)])
}

func (v Value) Keys() []string {
	x, ok := v.data.(dict)
	if !ok {
		strm, ok := v.data.(stream)
		if !ok {
			return nil
		}
		x = strm.hdr
	}
	keys := []string{}
	for k := range x {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	return keys
}

func (v Value) Index(i int) Value {
	x, ok := v.data.(array)
	if !ok || i < 0 || i >= len(x) {
		return Value{}
	}
	return v.r.resolve(v.ptr, x[i])
}

func (v Value) Len() int {
	x, ok := v.data.(array)
	if !ok {
		return 0
	}
	return len(x)
}

func (r *Reader) resolve(parent objptr, x interface{}) Value {
	if r != nil {
		r.budget.step(1)
	}

	if ptr, ok := x.(objptr); ok {
		r.budget.enter()
		defer r.budget.leave()
		if r.resolving[ptr] {
			panic(fmt.Errorf("malformed PDF: cyclic object reference"))
		}
		r.resolving[ptr] = true
		defer delete(r.resolving, ptr)

		if ptr.id >= uint32(len(r.xref)) {
			return Value{}
		}
		entry := r.xref[ptr.id]
		if !entry.resolves(ptr) {
			return Value{}
		}
		var obj object
		if entry.kind == xrefCompressed {
			strm := r.resolve(parent, entry.stream)
		Search:
			for {
				if strm.Kind() != Stream {
					panic("not a stream")
				}
				if strm.Key("Type").Name() != "ObjStm" {
					panic("not an object stream")
				}
				if value, found := r.streamObject(strm, ptr); found {
					x = value
					break Search
				}
				ext := strm.Key("Extends")
				if ext.Kind() != Stream {
					panic("cannot find object in stream")
				}
				strm = ext
			}
		} else {
			b := newBuffer(io.NewSectionReader(r.f, entry.offset, r.end-entry.offset), entry.offset, r.budget)
			b.key = r.key
			b.useAES = r.useAES
			obj = b.readObject()
			def, ok := obj.(objdef)
			if !ok {
				panic(fmt.Errorf("loading %v: found %T instead of objdef", ptr, obj))
			}
			if def.ptr != ptr {
				panic(fmt.Errorf("loading %v: found %v", ptr, def.ptr))
			}
			x = def.obj
		}
		parent = ptr
	}

	switch x := x.(type) {
	case nil, bool, int64, float64, name, dict, array, stream:
		return Value{r, parent, x}
	case string:
		return Value{r, parent, x}
	default:
		panic(fmt.Errorf("unexpected value type %T in resolve", x))
	}
}

func (r *Reader) streamObject(strm Value, ptr objptr) (object, bool) {
	n := strm.Key("N").Int64()
	r.budget.step(n)
	first := strm.Key("First").Int64()
	if first <= 0 {
		panic("missing First")
	}
	data := strm.Reader()
	defer data.Close()
	b := newBuffer(data, 0, r.budget)
	b.allowEOF = true
	for range n {
		id, validID := b.readToken().(int64)
		offset, validOffset := b.readToken().(int64)
		if !validID || id <= 0 || id > math.MaxUint32 {
			panic("invalid object stream identity")
		}
		if !validOffset || offset < 0 || offset > math.MaxInt64-first {
			panic("invalid object stream offset")
		}
		if uint32(id) == ptr.id {
			b.seekForward(first + offset)
			return b.readObject(), true
		}
	}
	return nil, false
}

type errorReadCloser struct {
	err error
}

func (e *errorReadCloser) Read([]byte) (int, error) {
	return 0, e.err
}

func (e *errorReadCloser) Close() error {
	return e.err
}

func (v Value) Reader() (result io.ReadCloser) {
	var closers []io.Closer
	defer func() {
		if result == nil {
			for _, closer := range closers {
				closer.Close()
			}
		}
	}()
	x, ok := v.data.(stream)
	if !ok {
		return &errorReadCloser{fmt.Errorf("stream not present")}
	}
	streamLen := v.Key("Length").Int64()
	if streamLen < 0 || x.offset < 0 || x.offset > v.r.end || streamLen > v.r.end-x.offset {
		panic(fmt.Errorf("malformed PDF: invalid stream length"))
	}
	if streamLen == 0 {
		return io.NopCloser(bytes.NewReader(nil))
	}
	var rd io.Reader
	rd = io.NewSectionReader(v.r.f, x.offset, streamLen)
	if v.r.key != nil {
		rd = decryptStream(v.r.key, v.r.useAES, x.ptr, rd)
	}
	filter := v.Key("Filter")
	param := v.Key("DecodeParms")
	switch filter.Kind() {
	default:
		panic(fmt.Errorf("unsupported filter %v", filter))
	case Null:
	case Name:
		rd = streamFilter(filter.Name()).decode(rd, param, v.r.budget, &closers)
	case Array:
		if filter.Len() > maxStreamFilters {
			panic(ErrBudget)
		}
		for i := 0; i < filter.Len(); i++ {
			rd = streamFilter(filter.Index(i).Name()).decode(rd, param.Index(i), v.r.budget, &closers)
		}
	}

	return &filteredReader{Reader: &budgetReader{source: rd, budget: v.r.budget}, closers: closers}
}

var passwordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// PDF encryption parameters. See PDF 32000-1:2008, §7.6.
const (
	minKeyBits = 40
	maxKeyBits = 128

	ouEntryLen = 32

	// md5KeyIterations is the number of MD5 rounds used to derive the file
	// key for revisions >= 3 (Algorithm 2, step (b)).
	md5KeyIterations = 50

	// rc4UIterations is the number of RC4 rounds used to derive U for
	// revisions >= 3 (Algorithm 5).
	rc4UIterations = 19
)

func (r *Reader) initEncrypt(userPassword []byte) error {
	encrypt, _ := r.resolve(objptr{}, r.trailer["Encrypt"]).data.(dict)
	if encrypt["Filter"] != name("Standard") {
		return errors.New("unsupported PDF: encryption filter")
	}
	n, _ := encrypt["Length"].(int64)
	if n == 0 {
		n = minKeyBits
	}
	if n%8 != 0 || n > maxKeyBits || n < minKeyBits {
		return fmt.Errorf("malformed PDF: %d-bit encryption key", n)
	}
	V, _ := encrypt["V"].(int64)
	if V != 1 && V != 2 && (V != 4 || !okayV4(encrypt)) {
		return fmt.Errorf("unsupported PDF: encryption version V=%d", V)
	}

	ids, ok := r.trailer["ID"].(array)
	if !ok || len(ids) < 1 {
		return fmt.Errorf("malformed PDF: missing ID in trailer")
	}
	idstr, ok := ids[0].(string)
	if !ok {
		return fmt.Errorf("malformed PDF: missing ID in trailer")
	}
	ID := []byte(idstr)

	R, _ := encrypt["R"].(int64)
	if R < 2 {
		return fmt.Errorf("malformed PDF: encryption revision R=%d", R)
	}
	if R > 4 {
		return fmt.Errorf("unsupported PDF: encryption revision R=%d", R)
	}
	O, _ := encrypt["O"].(string)
	U, _ := encrypt["U"].(string)
	if len(O) != ouEntryLen || len(U) != ouEntryLen {
		return fmt.Errorf("malformed PDF: missing O= or U= encryption parameters")
	}
	p, _ := encrypt["P"].(int64)
	P := uint32(p)

	h := md5.New()
	if len(userPassword) >= len(passwordPad) {
		h.Write(userPassword[:len(passwordPad)])
	} else {
		h.Write(userPassword)
		h.Write(passwordPad[:len(passwordPad)-len(userPassword)])
	}
	h.Write([]byte(O))
	h.Write([]byte{byte(P), byte(P >> 8), byte(P >> 16), byte(P >> 24)})
	h.Write(ID)
	if R >= 4 && encrypt["EncryptMetadata"] == false {
		h.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}
	key := h.Sum(nil)

	keyLen := int(n / 8)
	if R >= 3 {
		for i := 0; i < md5KeyIterations; i++ {
			h.Reset()
			h.Write(key[:keyLen])
			key = h.Sum(key[:0])
		}
		key = key[:keyLen]
	} else {
		key = key[:minKeyBits/8]
	}

	c, err := rc4.NewCipher(key)
	if err != nil {
		return fmt.Errorf("malformed PDF: invalid RC4 key: %w", err)
	}

	var u []byte
	if R == 2 {
		u = make([]byte, len(passwordPad))
		copy(u, passwordPad)
		c.XORKeyStream(u, u)
	} else {
		h.Reset()
		h.Write(passwordPad)
		h.Write([]byte(ID))
		u = h.Sum(nil)
		c.XORKeyStream(u, u)

		for i := 1; i <= rc4UIterations; i++ {
			key1 := make([]byte, len(key))
			copy(key1, key)
			for j := range key1 {
				key1[j] ^= byte(i)
			}
			c, _ = rc4.NewCipher(key1)
			c.XORKeyStream(u, u)
		}
	}

	if !bytes.HasPrefix([]byte(U), u) {
		return ErrInvalidPassword
	}

	r.key = key
	r.useAES = V == 4

	return nil
}

var ErrInvalidPassword = fmt.Errorf("encrypted PDF: invalid password")

func okayV4(encrypt dict) bool {
	cf, ok := encrypt["CF"].(dict)
	if !ok {
		return false
	}
	stmf, ok := encrypt["StmF"].(name)
	if !ok {
		return false
	}
	strf, ok := encrypt["StrF"].(name)
	if !ok {
		return false
	}
	if stmf != strf {
		return false
	}
	cfparam, ok := cf[stmf].(dict)
	if !ok {
		return false
	}
	if cfparam["AuthEvent"] != nil && cfparam["AuthEvent"] != name("DocOpen") {
		return false
	}
	if cfparam["Length"] != nil && cfparam["Length"] != int64(16) {
		return false
	}
	if cfparam["CFM"] != name("AESV2") {
		return false
	}
	return true
}

func (r *Reader) Page(num int) Page {
	num--
	page := r.Trailer().Key("Root").Key("Pages")
Search:
	for page.Key("Type").Name() == "Pages" {
		count := int(page.Key("Count").Int64())
		if count < num {
			return Page{}
		}
		kids := page.Key("Kids")
		for i := 0; i < kids.Len(); i++ {
			kid := kids.Index(i)
			if kid.Key("Type").Name() == "Pages" {
				c := int(kid.Key("Count").Int64())
				if num < c {
					page = kid
					continue Search
				}
				num -= c
				continue
			}
			if kid.Key("Type").Name() == "Page" {
				if num == 0 {
					return Page{kid}
				}
				num--
			}
		}
		break
	}
	return Page{}
}

func (r *Reader) NumPage() int {
	return int(r.Trailer().Key("Root").Key("Pages").Key("Count").Int64())
}
