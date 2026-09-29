package html_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	coremetadata "github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/html"
)

type pointerReader struct{}

func (*pointerReader) Read([]byte) (int, error) { return 0, io.EOF }

func metadataValue[T any](t *testing.T, values coremetadata.Map, key string) T {
	t.Helper()
	value, ok, err := values.Decode[T](key)
	if err != nil || !ok {
		t.Fatalf("metadata %q = (%v, %v)", key, ok, err)
	}
	return value
}

const samplePage = `<!doctype html>
<html>
<head>
  <title>Test Page</title>
  <meta name="description" content="An example page for the HTML reader.">
  <link rel="canonical" href="https://example.com/test">
  <style>.hidden { display: none }</style>
  <script>console.log('drop me');</script>
</head>
<body>
  <h1>Welcome</h1>
  <article>
    <h2>First Article</h2>
    <p>Hello, world.</p>
  </article>
  <article>
    <h2>Second Article</h2>
    <p>Goodbye, world.</p>
  </article>
  <script>alert('still drop me')</script>
</body>
</html>`

func TestWholePage(t *testing.T) {
	r, err := html.NewReader(strings.NewReader(samplePage), html.ReaderConfig{SourceName: "test.html"})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	body := docs[0].Text
	if !strings.Contains(body, "Hello, world.") || !strings.Contains(body, "Goodbye, world.") {
		t.Errorf("body missing expected text: %q", body)
	}
	if strings.Contains(body, "drop me") {
		t.Errorf("script content leaked into body: %q", body)
	}
	if got := metadataValue[string](t, docs[0].Metadata, html.MetadataTitle); got != "Test Page" {
		t.Errorf("title: want %q, got %v", "Test Page", got)
	}
	if got := metadataValue[string](t, docs[0].Metadata, html.MetadataCanonical); got != "https://example.com/test" {
		t.Errorf("canonical: got %v", got)
	}
	if got := metadataValue[string](t, docs[0].Metadata, html.MetadataSourceName); got != "test.html" {
		t.Errorf("source: got %v", got)
	}
}

func TestSelector(t *testing.T) {
	r, err := html.NewReader(
		strings.NewReader(samplePage),
		html.ReaderConfig{Selector: "article"},
	)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 articles, got %d", len(docs))
	}
	if !strings.Contains(docs[0].Text, "Hello, world.") {
		t.Errorf("docs[0]: %q", docs[0].Text)
	}
	if !strings.Contains(docs[1].Text, "Goodbye, world.") {
		t.Errorf("docs[1]: %q", docs[1].Text)
	}
	for i, d := range docs {
		if got := metadataValue[string](t, d.Metadata, html.MetadataSelector); got != "article" {
			t.Errorf("docs[%d] selector: got %v", i, got)
		}
	}
}

func TestReaderRejectsSourceBeyondBudget(t *testing.T) {
	budget, err := etl.NewSourceBudget(4)
	if err != nil {
		t.Fatal(err)
	}
	r, err := html.NewReader(strings.NewReader("12345"), html.ReaderConfig{SourceBudget: budget})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Read(t.Context())
	if docs != nil || !errors.Is(err, etl.ErrSourceTooLarge) {
		t.Fatalf("Read = (%v, %v), want nil ErrSourceTooLarge", docs, err)
	}
}

func TestNewReaderRejectsInvalidConfiguration(t *testing.T) {
	config := html.ReaderConfig{Selector: "["}
	if _, err := html.NewReader(strings.NewReader(samplePage), config); err == nil {
		t.Fatalf("NewReader(%+v) error = nil", config)
	}
}

func TestNewReaderRejectsNilSource(t *testing.T) {
	var typedNil *pointerReader
	if _, err := html.NewReader(nil, html.ReaderConfig{}); err == nil {
		t.Fatal("nil source must fail")
	}
	if _, err := html.NewReader(typedNil, html.ReaderConfig{}); err == nil {
		t.Fatal("typed nil source must fail")
	}
}

func TestCanonicalLinkMatchesRelTokenList(t *testing.T) {
	const page = `<html><head><link rel="Alternate CANONICAL" href="https://example.com/c"></head><body><p>text</p></body></html>`
	r, err := html.NewReader(strings.NewReader(page), html.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := r.Read(t.Context())
	if err != nil || len(docs) != 1 {
		t.Fatalf("Read = %v, %v", docs, err)
	}
	if got := metadataValue[string](t, docs[0].Metadata, html.MetadataCanonical); got != "https://example.com/c" {
		t.Fatalf("canonical = %q", got)
	}
}
