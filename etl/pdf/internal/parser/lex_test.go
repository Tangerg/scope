package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func parserBudget(t *testing.T) *budget {
	t.Helper()
	return newBudget(t.Context(), Limits{MaxWork: 1 << 20, MaxDecodedBytes: 1 << 20, MaxObjects: 1 << 14})
}

func parseOperand(t *testing.T, source string) (value object, err error) {
	t.Helper()
	defer func() {
		if failure := recover(); failure != nil {
			if cause, ok := failure.(error); ok {
				err = cause
			} else {
				err = fmt.Errorf("%v", failure)
			}
		}
	}()
	lexer := newBuffer(strings.NewReader(source), 0, parserBudget(t))
	lexer.allowEOF = true
	return lexer.readObject(), nil
}

func TestPDFOperandsPreserveTheSpecifiedSyntax(t *testing.T) {
	for _, sample := range []struct {
		source string
		want   object
	}{
		{"% comment\r\ntrue ", true}, {"false ", false}, {"null ", nil},
		{"42 ", int64(42)}, {"-17 ", int64(-17)}, {"+9 ", int64(9)},
		{"-1.25 ", -1.25}, {".5 ", 0.5}, {"2. ", 2.0},
		{"/A#20B ", name("A B")}, {"/ASCII#2fName ", name("ASCII/Name")},
		{"<61 62 63>", "abc"}, {"<aB0>", string([]byte{0xab, 0})}, {"<>", ""},
		{"(nested (text))", "nested (text)"}, {"(one\\ntwo\\r\\t\\b\\f\\(\\)\\\\)", "one\ntwo\r\t\b\f()\\"},
		{"(\\141\\12\\1)", "a\n\x01"}, {"(one\\\r\ntwo)", "onetwo"}, {"(line\r\nnext)", "line\nnext"},
		{"(\\q\\777)", "q\xff"}, {"(line\\\nnext)", "linenext"}, {"(line\rnext)", "line\nnext"},
		{"[1 /N true (x)]", array{int64(1), name("N"), true, "x"}},
		{"<< /A 2 /B [false null] >>", dict{name("A"): int64(2), name("B"): array{false, nil}}},
		{"12 3 R", objptr{12, 3}}, {"12 3 obj (hello) endobj", objdef{objptr{12, 3}, "hello"}},
	} {
		t.Run(sample.source, func(t *testing.T) {
			got, err := parseOperand(t, sample.source)
			if err != nil || !reflect.DeepEqual(got, sample.want) {
				t.Fatalf("operand=%#v error=%v; want %#v", got, err, sample.want)
			}
		})
	}
}

type readResult struct {
	data []byte
	err  error
}

func (r *readResult) Read(destination []byte) (int, error) {
	n := copy(destination, r.data)
	r.data = r.data[n:]
	return n, r.err
}

func TestLexerDoesNotLoseErrorsReturnedAlongsideData(t *testing.T) {
	cause := errors.Join(errors.New("decoded stream corrupt"), io.EOF)
	lexer := newBuffer(&readResult{data: []byte("(complete) "), err: cause}, 0, parserBudget(t))
	lexer.allowEOF = true
	if got := lexer.readObject(); got != "complete" {
		t.Fatalf("preceding operand=%v", got)
	}
	var err error
	func() {
		defer recoverTo(&err, func() {})
		lexer.readObject()
	}()
	if !errors.Is(err, cause) {
		t.Fatalf("pending stream failure=%v", err)
	}
	final := newBuffer(&readResult{data: []byte("(final)"), err: io.EOF}, 0, parserBudget(t))
	final.allowEOF = true
	if final.readObject() != "final" || !isEOF(final.readToken()) {
		t.Fatal("final bytes returned with EOF were not consumed")
	}
}

func TestLexerBoundsReadersThatMakeNoProgress(t *testing.T) {
	grant := newBudget(t.Context(), Limits{MaxWork: 5})
	lexer := newBuffer(&readResult{}, 0, grant)
	var err error
	func() {
		defer recoverTo(&err, func() {})
		lexer.readObject()
	}()
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("non-progressing reader=%v", err)
	}
}

func TestMalformedOperandsCannotBecomeValidObjects(t *testing.T) {
	for _, source := range []string{"<z0>", "<a", "[1", "<< /A 1", "<< 1 /A >>", "<< /A 1 >> stream invalid", ">", "1 0 obj (x) badend"} {
		if value, err := parseOperand(t, source); err == nil {
			t.Errorf("%q became %#v", source, value)
		}
	}
	_, err := parseOperand(t, strings.Repeat("[", maxNestingDepth+1))
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("nesting budget=%v", err)
	}
}

func TestStreamObjectsRespectAllLegalLineEndings(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		source := "1 0 obj << /Length 3 >> stream" + newline + "abc\nendstream\nendobj"
		parsed, err := parseOperand(t, source)
		if err != nil {
			t.Fatal(err)
		}
		value, ok := parsed.(objdef)
		if !ok {
			t.Fatalf("object=%T", parsed)
		}
		data, ok := value.obj.(stream)
		if !ok || data.ptr != value.ptr || !bytes.HasPrefix([]byte(source)[data.offset:], []byte("abc")) {
			t.Fatalf("stream offset=%#v", value.obj)
		}
	}
}
