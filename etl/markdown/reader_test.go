package markdown_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/markdown"
)

type pointerReader struct{}

func (*pointerReader) Read([]byte) (int, error) { return 0, io.EOF }

func metadataValue[T any](t *testing.T, values coremetadata.Map, key string) (T, bool) {
	t.Helper()
	value, ok, err := values.Decode[T](key)
	if err != nil {
		t.Fatalf("metadata %q: %v", key, err)
	}
	return value, ok
}

const sample = `# Intro

Some intro paragraph with **bold**.

## Section A

Body of section A.

### A.1 subsection

Nested body — should stay inside A when split at H2.

## Section B

Body of section B.
`

func TestWholeDocument(t *testing.T) {
	r, err := markdown.NewReader(strings.NewReader(sample), markdown.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("whole mode: want 1 doc, got %d", len(docs))
	}
	if !strings.Contains(docs[0].Text, "Section B") {
		t.Errorf("whole-doc body missing Section B; got: %q", docs[0].Text)
	}
}

func TestReaderRejectsSourceBeyondBudget(t *testing.T) {
	budget, err := etl.NewSourceBudget(4)
	if err != nil {
		t.Fatal(err)
	}
	r, err := markdown.NewReader(strings.NewReader("12345"), markdown.ReaderConfig{SourceBudget: budget})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Read(t.Context())
	if docs != nil || !errors.Is(err, etl.ErrSourceTooLarge) {
		t.Fatalf("Read = (%v, %v), want nil ErrSourceTooLarge", docs, err)
	}
}

func TestHeadingSplitH2(t *testing.T) {
	r, err := markdown.NewReader(
		strings.NewReader(sample),
		markdown.ReaderConfig{HeadingSplitLevel: 2, SourceName: "test.md"},
	)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("split mode: want 3 docs, got %d", len(docs))
	}

	want := []struct {
		heading string
		path    string
	}{
		{"Intro", "Intro"},
		{"Section A", "Intro > Section A"},
		{"Section B", "Intro > Section B"},
	}
	for i, w := range want {
		if got, _ := metadataValue[string](t, docs[i].Metadata, markdown.MetadataHeading); got != w.heading {
			t.Errorf("docs[%d] heading: want %q, got %v", i, w.heading, got)
		}
		if got, _ := metadataValue[string](t, docs[i].Metadata, markdown.MetadataHeadingPath); got != w.path {
			t.Errorf("docs[%d] path: want %q, got %v", i, w.path, got)
		}
		if got, _ := metadataValue[string](t, docs[i].Metadata, markdown.MetadataSourceName); got != "test.md" {
			t.Errorf("docs[%d] source: want test.md, got %v", i, got)
		}
	}

	if !strings.Contains(docs[1].Text, "Nested body") {
		t.Errorf("section A missing nested H3 body; got: %q", docs[1].Text)
	}
}

func TestHeadingSplitPreservesMarkdownAndFormattedHeadingText(t *testing.T) {
	source := "# **Bold** and [`code`](https://example.com)\n\nBody with  double spaces and **markup**.\n"
	r, err := markdown.NewReader(strings.NewReader(source), markdown.ReaderConfig{HeadingSplitLevel: 1})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("Read returned %d documents, want 1", len(docs))
	}
	if docs[0].Text != strings.TrimSpace(source) {
		t.Fatalf("document text = %q, want original Markdown %q", docs[0].Text, strings.TrimSpace(source))
	}
	if got, _ := metadataValue[string](t, docs[0].Metadata, markdown.MetadataHeading); got != "Bold and code" {
		t.Fatalf("heading metadata = %q, want %q", got, "Bold and code")
	}
}

func TestNewReaderRejectsInvalidConfiguration(t *testing.T) {
	for _, config := range []markdown.ReaderConfig{
		{HeadingSplitLevel: -1},
		{HeadingSplitLevel: 7},
	} {
		if _, err := markdown.NewReader(strings.NewReader(sample), config); err == nil {
			t.Fatalf("NewReader(%+v) error = nil", config)
		}
	}
}

func TestNewReaderRejectsNilSource(t *testing.T) {
	var typedNil *pointerReader
	if _, err := markdown.NewReader(nil, markdown.ReaderConfig{}); err == nil {
		t.Fatal("nil source must fail")
	}
	if _, err := markdown.NewReader(typedNil, markdown.ReaderConfig{}); err == nil {
		t.Fatal("typed nil source must fail")
	}
}
