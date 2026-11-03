package pdf_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/pdf"
)

func TestTextLineOperatorsDoNotBecomeFontEncodedBytes(t *testing.T) {
	for _, sample := range []struct{ encoding, operators, want string }{
		{"", "(first) Tj (second) ' 1 2 (third) \" [(fourth) -20 ( fifth)] TJ", "first\nsecond\nthirdfourth fifth"},
		{"/Encoding /UniGB-UCS2-H", "<0041> Tj T* <0042> Tj", "A\nB"},
	} {
		stream := "BT /F1 12 Tf " + sample.operators + " ET"
		payload := encodePDFObjects([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica " + sample.encoding + " >>",
		})
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{})
		if err != nil {
			t.Fatal(err)
		}
		docs, err := reader.Read(t.Context())
		if err != nil || len(docs) != 1 || strings.TrimSpace(docs[0].Text) != sample.want {
			t.Fatalf("docs=%v error=%v want=%q", docs, err, sample.want)
		}
	}
}
