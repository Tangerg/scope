package etl_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	etljson "github.com/Tangerg/scope/etl/json"
	"github.com/Tangerg/scope/etl/markdown"
	"github.com/Tangerg/scope/etl/text"
)

func revision(raw string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw))) }
func TestNamedWholeTextAndMarkdownBindOriginalBytes(t *testing.T) {
	raw := "  α\r\n"
	for _, parse := range []func(context.Context) ([]*document.Document, error){
		func(ctx context.Context) ([]*document.Document, error) {
			r, err := text.NewReader(strings.NewReader(raw), text.ReaderConfig{SourceName: "original"})
			if err != nil {
				return nil, err
			}
			return r.Read(ctx)
		},
		func(ctx context.Context) ([]*document.Document, error) {
			r, err := markdown.NewReader(strings.NewReader(raw), markdown.ReaderConfig{SourceName: "original"})
			if err != nil {
				return nil, err
			}
			return r.Read(ctx)
		},
	} {
		ds, err := parse(t.Context())
		if err != nil || len(ds) != 1 {
			t.Fatalf("read: %v %v", ds, err)
		}
		p := ds[0].Provenance
		if p == nil || p.Source != "original" || p.Revision != revision(raw) || p.Bytes == nil || *p.Bytes != (document.ByteRange{End: int64(len(raw))}) || len(p.Chunks) != 0 {
			t.Fatalf("original: %+v", p)
		}
	}
}
func TestJSONUsesActualOffsetsForRepeatedValues(t *testing.T) {
	raw := " \t[  \"同\" , \"同\", 1e400 ]\n"
	r, err := etljson.NewReader(strings.NewReader(raw), etljson.ReaderConfig{SourceName: "data.json"})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := r.Read(t.Context())
	if err != nil || len(ds) != 3 {
		t.Fatalf("read: %v %v", ds, err)
	}
	var last int64
	for i, d := range ds {
		p := d.Provenance
		if p == nil || p.Bytes == nil || p.Bytes.Start < last || raw[p.Bytes.Start:p.Bytes.End] != d.Text || p.Revision != revision(raw) || !slices.Equal(p.Chunks, []document.Chunk{{Index: i, Total: 3}}) {
			t.Fatalf("element %d: %s %+v", i, d.Text, p)
		}
		last = p.Bytes.End
	}
}
func TestMarkdownSectionsDoNotClaimRenderedByteOffsets(t *testing.T) {
	raw := "# First\n\none\n\n# Second\n\ntwo\n"
	facts := metadata.Map{}
	_ = facts.Set("markdown.source", "business")
	r, err := markdown.NewReader(strings.NewReader(raw), markdown.ReaderConfig{SourceName: "book.md", HeadingSplitLevel: 1, Metadata: facts})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := r.Read(t.Context())
	if err != nil || len(ds) != 2 {
		t.Fatalf("sections=%v %v", ds, err)
	}
	for i, d := range ds {
		p := d.Provenance
		if p == nil || p.Bytes != nil || p.Page != 0 || p.Source != "book.md" || p.Revision != revision(raw) || !slices.Equal(p.Chunks, []document.Chunk{{Index: i, Total: 2}}) {
			t.Fatalf("section %d: %+v", i, p)
		}
		if v, _, _ := d.Metadata.Decode[string]("markdown.source"); v != "business" {
			t.Fatal(d.Metadata)
		}
	}
}
