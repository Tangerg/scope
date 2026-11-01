package html_test

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl/html"
)

func TestSelectorKeepsOriginalRevisionWithoutTrustingCanonicalURL(t *testing.T) {
	raw := `<html><head><link rel="canonical" href="https://untrusted.invalid/"></head><body><p>One   sentence</p><p> </p><p>Two</p></body></html>`
	facts := metadata.Map{}
	_ = facts.Set("html.source", "client")
	r, err := html.NewReader(strings.NewReader(raw), html.ReaderConfig{SourceName: "host-source", Selector: "p", Metadata: facts})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := r.Read(t.Context())
	if err != nil || len(ds) != 2 {
		t.Fatalf("read: %v %v", ds, err)
	}
	for i, d := range ds {
		p := d.Provenance
		if p == nil || p.Source != "host-source" || p.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw))) || p.Bytes != nil || !slices.Equal(p.Chunks, []document.Chunk{{Index: i, Total: 2}}) {
			t.Fatalf("element %d: %+v", i, p)
		}
		if got, _, _ := d.Metadata.Decode[string]("html.source"); got != "client" {
			t.Fatal(d.Metadata)
		}
	}
}
