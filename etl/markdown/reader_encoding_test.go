package markdown_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl/markdown"
)

func TestReaderRejectsInvalidContentAndMetadataWithoutPartialDocuments(t *testing.T) {
	for _, level := range []int{0, 1} {
		for _, test := range []struct {
			name       string
			content    string
			sourceName string
		}{
			{name: "body", content: "# Valid\n\nvalid\n\n# Invalid\n\nbad\xff"},
			{name: "heading", content: "# bad\xff\n\ntext"},
			{name: "source", content: "# Valid\n\ntext", sourceName: "bad\xff"},
		} {
			t.Run(test.name, func(t *testing.T) {
				reader, err := markdown.NewReader(strings.NewReader(test.content), markdown.ReaderConfig{HeadingSplitLevel: level, SourceName: test.sourceName})
				if test.sourceName != "" {
					if reader != nil || !errors.Is(err, document.ErrInvalidDocument) {
						t.Fatalf("invalid source admitted: reader=%v error=%v", reader, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if docs, err := reader.Read(t.Context()); docs != nil || err == nil {
					t.Fatalf("level=%d Read = %v, %v", level, docs, err)
				}
			})
		}
	}
}
