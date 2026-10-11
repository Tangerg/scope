package html_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/etl/html"
)

func ExampleReader() {
	const page = `<html><head><title>Scope</title></head>` +
		`<body><h1>Readers</h1><p>Scope turns HTML into documents.</p></body></html>`
	reader, err := html.NewReader(strings.NewReader(page), html.ReaderConfig{})
	if err != nil {
		panic(err)
	}
	docs, err := reader.Read(context.Background())
	if err != nil {
		panic(err)
	}
	values, err := docs[0].Metadata.Values()
	if err != nil {
		panic(err)
	}
	fmt.Println(docs[0].Text)
	fmt.Println(values[html.MetadataTitle])
	// Output:
	// Readers Scope turns HTML into documents.
	// Scope
}
