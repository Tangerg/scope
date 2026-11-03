package etl_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/etl"
)

func TestNestedSplitsPreserveLineageAndBusinessMetadata(t *testing.T) {
	assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{Generator: etl.NewSHA256IDGenerator(nil)})
	if err != nil {
		t.Fatal(err)
	}
	split := func(_ context.Context, s string) ([]string, error) { return strings.Split(s, " "), nil }
	splitter, err := etl.NewSplitter(etl.SplitterConfig{TextSplitter: etl.TextSplitterFunc(split)})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("word  word")
	facts := metadata.Map{}
	for _, key := range []string{"parent_document_id", "chunk_index", "chunk_total"} {
		if setErr := facts.Set(key, "tenant value"); setErr != nil {
			t.Fatal(setErr)
		}
	}
	p := document.NewProvenance("book", original)
	p.Page = 3
	p.Bytes = &document.ByteRange{End: int64(len(original))}
	parent := &document.Document{ID: "edition", Text: string(original), Provenance: p, Metadata: facts}
	docs, err := splitter.Split(t.Context(), []*document.Document{parent})
	if err != nil || len(docs) != 2 {
		t.Fatalf("first split: %v %v", docs, err)
	}
	if docs, err = assigner.Assign(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	twice, err := splitter.Split(t.Context(), docs)
	if err != nil || len(twice) != 2 {
		t.Fatalf("second split: %v %v", twice, err)
	}
	if docs[0].ID == docs[1].ID {
		t.Fatal("identical text occurrences received the same store identity")
	}
	for i, d := range twice {
		q := d.Provenance
		want := []document.Chunk{{Parent: "edition", Index: i, Total: 2}, {Parent: docs[i].ID, Index: 0, Total: 1}}
		if q == nil || q.Source != p.Source || q.Revision != p.Revision || q.Page != 3 || q.Bytes != nil || !slices.Equal(q.Chunks, want) || !facts.Equal(d.Metadata) {
			t.Fatalf("nested chunk: %+v %v", q, d.Metadata)
		}
		q.Chunks[0].Parent = "changed"
		if docs[i].Provenance.Chunks[0].Parent != "edition" {
			t.Fatal("child aliased its parent")
		}
	}
	if p.Bytes == nil || len(p.Chunks) != 0 {
		t.Fatal("source location was overwritten")
	}
}
func TestAnonymousInputCanBeSplitWithoutClaimingAnOriginal(t *testing.T) {
	splitter, err := etl.NewSplitter(etl.SplitterConfig{TextSplitter: etl.TextSplitterFunc(func(_ context.Context, s string) ([]string, error) { return []string{s}, nil })})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := splitter.Split(t.Context(), []*document.Document{{Text: "anonymous"}})
	if err != nil {
		t.Fatal(err)
	}
	d := docs[0]
	if d.ID != "" || d.Metadata != nil || d.Provenance.Source != "" || d.Provenance.Revision != "" || !slices.Equal(d.Provenance.Chunks, []document.Chunk{{Index: 0, Total: 1}}) {
		t.Fatalf("invented identity: %+v", d)
	}
}
