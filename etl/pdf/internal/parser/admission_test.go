package parser

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func encryptedDocument(encryption string) []byte {
	var source bytes.Buffer
	source.WriteString("%PDF-1.4\n")
	offset := source.Len()
	fmt.Fprintf(&source, "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 1 /ID [(test)] /Encrypt %s >>\nstartxref\n%d\n%%%%EOF\n", encryption, offset)
	return source.Bytes()
}

func TestMalformedSecurityDictionariesDoNotLookLikeWrongPasswords(t *testing.T) {
	for _, encryption := range []string{
		"<< /Filter /Other >>",
		"<< /Filter /Standard /Length 41 >>",
		"<< /Filter /Standard /V 5 >>",
		"<< /Filter /Standard /V 1 /R 1 >>",
		"<< /Filter /Standard /V 1 /R 5 >>",
		"<< /Filter /Standard /V 1 /R 2 >>",
		"<< /Filter /Standard /V 4 >>",
		"<< /Filter /Standard /V 4 /CF << >> >>",
		"<< /Filter /Standard /V 4 /CF << >> /StmF /StdCF >>",
		"<< /Filter /Standard /V 4 /CF << >> /StmF /StdCF /StrF /Other >>",
		"<< /Filter /Standard /V 4 /CF << >> /StmF /StdCF /StrF /StdCF >>",
		"<< /Filter /Standard /V 4 /CF << /StdCF << /AuthEvent /Other >> >> /StmF /StdCF /StrF /StdCF >>",
		"<< /Filter /Standard /V 4 /CF << /StdCF << /Length 1 >> >> /StmF /StdCF /StrF /StdCF >>",
		"<< /Filter /Standard /V 4 /CF << /StdCF << /CFM /Other >> >> /StmF /StdCF /StrF /StdCF >>",
	} {
		source := encryptedDocument(encryption)
		reader, err := NewReader(t.Context(), bytes.NewReader(source), int64(len(source)), parserBudget(t).limits, nil)
		if reader != nil || err == nil || errors.Is(err, ErrInvalidPassword) {
			t.Fatalf("encryption=%s reader=%v error=%v", encryption, reader, err)
		}
	}
}

func TestXrefAndHeaderAdmissionRejectsMalformedDocuments(t *testing.T) {
	for _, source := range []string{
		"%PDF-1.4\ntruncated",
		"%PDF-1.4\n%%EOF",
		"%PDF-1.4\nstartxref\nnull\n%%EOF",
		"%PDF-1.4\nstartxref\n9999999\n%%EOF",
		"%PDF-1.4\nnull\nstartxref\n9\n%%EOF",
		"%PDF-1.4\n1 0 obj (not-stream) endobj\nstartxref\n9\n%%EOF",
		"%PDF-1.4\n1 0 obj << /Length 0 >> stream\nendstream\nendobj\nstartxref\n9\n%%EOF",
		"%PDF-1.4\n1 0 obj << /Type /XRef /Length 0 >> stream\nendstream\nendobj\nstartxref\n9\n%%EOF",
	} {
		if reader, err := NewReader(t.Context(), strings.NewReader(source), int64(len(source)), parserBudget(t).limits, nil); reader != nil || err == nil {
			t.Fatalf("malformed document admitted: %q", source)
		}
	}
}

func TestCMapDefinitionsRequireAnOpenDictionaryAndName(t *testing.T) {
	for _, source := range []string{"/Key 1 def", "1 dict begin 1 2 def end"} {
		var err error
		func() {
			defer recoverTo(&err, func() {})
			Interpret(testStream(t, []byte(source), nil), Value{}, func(*Stack, string) {})
		}()
		if err == nil {
			t.Fatalf("malformed dictionary definition accepted: %s", source)
		}
	}
}
