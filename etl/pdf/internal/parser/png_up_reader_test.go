package parser

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
)

func TestPNGUpUsesDeclaredDimensionsAndDefaults(t *testing.T) {
	for _, sample := range []struct {
		parameters dict
		encoded    []byte
		decoded    []byte
	}{
		{nil, []byte{2, 3, 2, 4}, []byte{3, 7}},
		{dict{"Colors": int64(3)}, []byte{2, 1, 2, 3, 2, 1, 1, 1}, []byte{1, 2, 3, 2, 3, 4}},
		{dict{"BitsPerComponent": int64(4), "Columns": int64(3)}, []byte{2, 0x12, 0x30}, []byte{0x12, 0x30}},
		{dict{"BitsPerComponent": int64(16)}, []byte{2, 0x12, 0x34}, []byte{0x12, 0x34}},
	} {
		reader := newPNGUpReader(bytes.NewReader(sample.encoded), literalValue(t, sample.parameters), parserBudget(t))
		got, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(got, sample.decoded) {
			t.Fatalf("parameters=%v decoded=%v error=%v", sample.parameters, got, err)
		}
	}
}

func TestPNGUpRejectsMalformedAndUnboundedDimensions(t *testing.T) {
	for _, parameters := range []dict{
		{"Columns": int64(0)}, {"Colors": int64(-1)}, {"Colors": name("3")},
		{"BitsPerComponent": int64(3)},
		{"Columns": int64(math.MaxInt64), "Colors": int64(2)},
		{"Columns": int64(math.MaxInt64 / 2), "BitsPerComponent": int64(16)},
		{"Columns": int64(1 << 20)},
	} {
		var err error
		func() {
			defer recoverTo(&err, func() {})
			newPNGUpReader(bytes.NewReader(nil), literalValue(t, parameters), parserBudget(t))
		}()
		if err == nil {
			t.Fatalf("invalid dimensions accepted: %v", parameters)
		}
	}
}

func TestPNGTailFailureCannotBecomeCleanLexerEOF(t *testing.T) {
	reader := newPNGUpReader(bytes.NewReader([]byte{2, '(', ')', 1, 'x', 'x'}), literalValue(t, dict{"Columns": int64(2)}), parserBudget(t))
	lexer := newBuffer(reader, 0, parserBudget(t))
	lexer.allowEOF = true
	if got := lexer.readObject(); got != "" {
		t.Fatalf("first complete operand=%v", got)
	}
	var err error
	func() {
		defer recoverTo(&err, func() {})
		lexer.readObject()
	}()
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("malformed PNG tail hidden: %v", err)
	}
}
