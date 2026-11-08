package html_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/html"
)

func TestHiddenSubtreesAreExcludedFromDefaultAndSelectedText(t *testing.T) {
	for _, hidden := range []string{
		`<p hidden>hidden text</p>`,
		`<p hidden="hidden">hidden text</p>`,
		`<p hidden="false">hidden text</p>`,
		`<p hidden="until-found">hidden text</p>`,
		`<section hidden><p>hidden text</p></section>`,
	} {
		for _, selector := range []string{"", "p"} {
			t.Run(hidden+"/"+selector, func(t *testing.T) {
				reader, err := html.NewReader(strings.NewReader(hidden+`<p>visible text</p>`), html.ReaderConfig{Selector: selector, SourceName: "source.html"})
				if err != nil {
					t.Fatal(err)
				}
				documents, err := reader.Read(t.Context())
				if err != nil || len(documents) != 1 {
					t.Fatalf("Read = %v, %v", documents, err)
				}
				if documents[0].Text != "visible text" || documents[0].Provenance.Source != "source.html" {
					t.Fatalf("document = %#v", documents[0])
				}
			})
		}
	}
}

func TestSelectedDescendantsRetainNonContentAncestorExclusion(t *testing.T) {
	reader, err := html.NewReader(strings.NewReader(`<html><head><title>page title</title></head><body><p>body</p></body></html>`), html.ReaderConfig{Selector: "title"})
	if err != nil {
		t.Fatal(err)
	}
	documents, err := reader.Read(t.Context())
	if err != nil || len(documents) != 0 {
		t.Fatalf("Read = %v, %v; want no selected body text", documents, err)
	}
}
