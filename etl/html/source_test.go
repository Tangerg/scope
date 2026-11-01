package html_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl/html"
)

func TestSequentialSourceDoesNotPublishPartialHTML(t *testing.T) {
	failure := errors.New("source disconnected")
	for _, sel := range []string{"", "p"} {
		t.Run("read error/"+sel, func(t *testing.T) {
			source := io.MultiReader(strings.NewReader("<p>complete-looking paragraph</p>"), iotest.ErrReader(failure))
			r, err := html.NewReader(source, html.ReaderConfig{Selector: sel, SourceName: "report"})
			if err != nil {
				t.Fatal(err)
			}
			if ds, readErr := r.Read(t.Context()); ds != nil || !errors.Is(readErr, failure) {
				t.Fatalf("partial publication: %v, %v", ds, readErr)
			}
		})
		t.Run("parser rejection/"+sel, func(t *testing.T) {
			// x/net/html's parser rejects an open element stack deeper than 512.
			raw := "<p>earlier match</p>" + strings.Repeat("<section>", 600)
			r, err := html.NewReader(strings.NewReader(raw), html.ReaderConfig{Selector: sel, SourceName: "report"})
			if err != nil {
				t.Fatal(err)
			}
			if ds, readErr := r.Read(t.Context()); ds != nil || readErr == nil {
				t.Fatalf("rejected DOM returned a document: %v, %v", ds, readErr)
			}
		})
	}
}

func TestPreCanceledSourceCanBeReadWithoutChangingTheOriginalRevision(t *testing.T) {
	const raw = "<html><body><p>Hello &amp; goodbye</p></body></html>"
	r, err := html.NewReader(strings.NewReader(raw), html.ReaderConfig{SourceName: "archive/example"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if ds, readErr := r.Read(ctx); ds != nil || !errors.Is(readErr, context.Canceled) {
		t.Fatalf("pre-canceled read: %v, %v", ds, readErr)
	}
	ds, err := r.Read(t.Context())
	if err != nil || len(ds) != 1 || ds[0].Text != "Hello & goodbye" {
		t.Fatalf("fresh read: %v, %v", ds, err)
	}
	want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))
	if p := ds[0].Provenance; p == nil || p.Source != "archive/example" || p.Revision != want || p.Bytes != nil || len(p.Chunks) != 0 {
		t.Fatalf("rendered page claimed different evidence: %+v", p)
	}
}
func TestFrozenPageAndSiblingFragmentsOwnIndependentData(t *testing.T) {
	const raw = `<head><title>Original</title></head><body><p>a</p><p> </p><p>b</p></body>`
	facts := mustMetadata(t, map[string]any{"tenant": "blue"})
	r, err := html.NewReader(strings.NewReader(raw), html.ReaderConfig{SourceName: "report", Selector: "p", Metadata: facts})
	if err != nil {
		t.Fatal(err)
	}
	facts["tenant"][1] = 'X'
	ds, err := r.Read(t.Context())
	if err != nil || len(ds) != 2 {
		t.Fatalf("fragments: %v, %v", ds, err)
	}
	if metadataValue[string](t, ds[0].Metadata, "tenant") != "blue" {
		t.Fatal("reader retained caller metadata")
	}
	ds[0].Metadata["tenant"][1] = 'Y'
	ds[0].Metadata[html.MetadataTitle][1] = 'Y'
	ds[0].Provenance.Chunks[0].Index = 9
	second := ds[1]
	if second.Text != "b" || metadataValue[string](t, second.Metadata, "tenant") != "blue" || metadataValue[string](t, second.Metadata, html.MetadataTitle) != "Original" || len(second.Provenance.Chunks) != 1 || second.Provenance.Chunks[0] != (document.Chunk{Index: 1, Total: 2}) {
		t.Fatalf("editing a sibling changed text, metadata, or lineage: %+v", second)
	}
}
func TestEmptyInputAndPagesWithoutVisibleTextReturnNoDocuments(t *testing.T) {
	for _, raw := range []string{" \t\n", `<head><title>Only metadata</title></head><script>unseen</script>`} {
		for _, sel := range []string{"", "article"} {
			r, err := html.NewReader(strings.NewReader(raw), html.ReaderConfig{SourceName: "report", Selector: sel})
			if err != nil {
				t.Fatal(err)
			}
			if ds, readErr := r.Read(t.Context()); len(ds) != 0 || readErr != nil {
				t.Fatalf("empty extraction returned %v, %v", ds, readErr)
			}
		}
	}
}
