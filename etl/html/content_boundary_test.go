package html_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/html"
)

func TestInvalidUTF8CannotEscapeThroughContentOrMetadata(t *testing.T) {
	for _, selector := range []string{"", "p"} {
		for _, test := range []struct {
			name       string
			content    string
			sourceName string
		}{
			{name: "text", content: "<p>bad\xff</p>"},
			{name: "title", content: "<head><title>bad\xff</title></head><body><p>text</p></body>"},
			{name: "source", content: "<p>text</p>", sourceName: "bad\xff"},
		} {
			t.Run(test.name+"/"+selector, func(t *testing.T) {
				reader, err := html.NewReader(strings.NewReader(test.content), html.ReaderConfig{Selector: selector, SourceName: test.sourceName})
				if err != nil {
					t.Fatal(err)
				}
				if docs, err := reader.Read(t.Context()); docs != nil || err == nil {
					t.Fatalf("Read = %v, %v", docs, err)
				}
			})
		}
	}
}
