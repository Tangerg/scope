package pdf_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/etl/pdf"
)

func TestIncrementalDeletionDoesNotExtractHistoricalContent(t *testing.T) {
	base := testPDF("deleted content")
	var previous int
	if _, err := fmt.Sscanf(string(base[bytes.LastIndex(base, []byte("startxref")):]), "startxref\n%d", &previous); err != nil {
		t.Fatal(err)
	}
	var source bytes.Buffer
	source.Write(base)
	offset := source.Len()
	fmt.Fprintf(&source, "xref\n0 1\n0000000004 65535 f \n4 1\n0000000000 00001 f \ntrailer\n<< /Size 6 /Root 1 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", previous, offset)

	for _, perPage := range []bool{false, true} {
		reader, err := pdf.NewReader(bytes.NewReader(source.Bytes()), int64(source.Len()), pdf.ReaderConfig{PerPage: perPage})
		if err != nil {
			t.Fatal(err)
		}
		documents, err := reader.Read(t.Context())
		if err != nil || len(documents) != 0 {
			t.Fatalf("perPage=%t deleted content returned as documents: %v, %v", perPage, documents, err)
		}
	}
}
