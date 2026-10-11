package pdf_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/etl/pdf"
)

func ExampleReader() {
	// testPDF builds a complete two-page PDF 1.4 stream in memory.
	payload := testPDF("Scope extracts PDF text", "page two")
	reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{})
	if err != nil {
		panic(err)
	}
	docs, err := reader.Read(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(len(docs))
	fmt.Println(strings.Contains(docs[0].Text, "Scope extracts PDF text"))
	// Output:
	// 1
	// true
}
