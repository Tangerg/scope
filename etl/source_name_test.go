package etl_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	etljson "github.com/Tangerg/scope/etl/json"
	"github.com/Tangerg/scope/etl/markdown"
	"github.com/Tangerg/scope/etl/text"
)

type sourceDocumentReader interface {
	Read(context.Context) ([]*document.Document, error)
}

func TestInvalidSourceNameIsRejectedWithoutConsumingInput(t *testing.T) {
	factories := []struct {
		name      string
		newReader func(io.Reader, string) (sourceDocumentReader, error)
	}{
		{"text", func(source io.Reader, name string) (sourceDocumentReader, error) {
			return text.NewReader(source, text.ReaderConfig{SourceName: name})
		}},
		{"json", func(source io.Reader, name string) (sourceDocumentReader, error) {
			return etljson.NewReader(source, etljson.ReaderConfig{SourceName: name})
		}},
		{"markdown", func(source io.Reader, name string) (sourceDocumentReader, error) {
			return markdown.NewReader(source, markdown.ReaderConfig{SourceName: name})
		}},
	}
	for _, factory := range factories {
		t.Run(factory.name, func(t *testing.T) {
			for _, name := range []string{"\xff", " \t\n", "\u2003"} {
				const content = `"valuable source"`
				source := strings.NewReader(content)
				reader, err := factory.newReader(source, name)
				if !lo.IsNil(reader) || !errors.Is(err, document.ErrInvalidDocument) || source.Len() != len(content) {
					t.Fatalf("name=%q reader=%v unread=%d error=%v", name, reader, source.Len(), err)
				}
				reader, err = factory.newReader(source, "usable-source")
				if err != nil {
					t.Fatal(err)
				}
				docs, err := reader.Read(t.Context())
				if err != nil || len(docs) != 1 || docs[0].Text != content || docs[0].Provenance.Source != "usable-source" {
					t.Fatalf("reopened source: documents=%v error=%v", docs, err)
				}
			}
		})
	}
}
