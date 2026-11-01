package document_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
)

func TestProvenanceTracksOriginalAcrossTransforms(t *testing.T) {
	const source = "urn:manual:power"
	origin := document.NewProvenance(source, []byte("aébc"))
	if origin.Revision != document.NewProvenance(source, []byte("aébc")).Revision ||
		origin.Revision == document.NewProvenance(source, []byte("Aébc")).Revision {
		t.Fatal("revision did not bind the original bytes")
	}
	origin.Page = 2
	origin.Bytes = &document.ByteRange{Start: 0, End: 5}
	part := origin.Split("page-2", 1, 3)
	nested := part.Split("section", 0, 2)
	if nested.Source != source || nested.Revision != origin.Revision || nested.Page != 2 || nested.Bytes != nil ||
		!reflect.DeepEqual(nested.Chunks, []document.Chunk{{Parent: "page-2", Index: 1, Total: 3}, {Parent: "section", Index: 0, Total: 2}}) {
		t.Fatalf("derived evidence = %+v", nested)
	}
	base := &document.Document{Text: "é", Provenance: origin}
	clone := base.Clone()
	clone.Provenance.Bytes.End = 3
	nested.Chunks[0].Parent = "edited"
	if origin.RequireRevision() != nil || part.Chunks[0].Parent != "page-2" || origin.Bytes.End != 5 || len(origin.Chunks) != 0 {
		t.Fatal("derived or cloned document changed source evidence")
	}
	data, marshalErr := jsonv2.Marshal(base)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var restored document.Document
	if decodeErr := jsonv2.Unmarshal(data, &restored); decodeErr != nil || !reflect.DeepEqual(base, &restored) {
		t.Fatalf("document round trip = %+v, %v", restored, decodeErr)
	}
}

func TestProvenanceRejectsUnprovableCoordinates(t *testing.T) {
	for _, p := range []*document.Provenance{
		{}, {Source: "source"}, {Revision: "v1"}, {Source: " ", Revision: "v1"},
		{Source: "source", Revision: "\xff"}, {Page: -1},
		{Bytes: &document.ByteRange{Start: -1, End: 2}}, {Bytes: &document.ByteRange{Start: 2, End: 1}},
		{Chunks: []document.Chunk{{Index: 0, Total: 0}}}, {Chunks: []document.Chunk{{Index: -1, Total: 1}}},
		{Chunks: []document.Chunk{{Parent: "\xff", Total: 1}}},
	} {
		doc := &document.Document{Text: "excerpt", Provenance: p}
		if err := p.RequireRevision(); !errors.Is(err, document.ErrInvalidDocument) {
			t.Fatalf("invalid source admitted: %+v, %v", p, err)
		}
		if err := doc.Validate(); !errors.Is(err, document.ErrInvalidDocument) {
			t.Fatalf("accepted %+v: %v", p, err)
		}
	}
	anonymous := document.NewProvenance("", []byte("same text"))
	local := anonymous.Split("", 0, 1)
	if !errors.Is(anonymous.RequireRevision(), document.ErrUnversionedSource) || !errors.Is(local.RequireRevision(), document.ErrUnversionedSource) || anonymous != nil || local.Source != "" || local.Revision != "" || local.Validate() != nil || anonymous.Clone() != nil {
		t.Fatal("anonymous transform invented an original or lost known relative position")
	}
	span := &document.Provenance{Bytes: &document.ByteRange{Start: 0, End: 2}}
	if span.Excerpt() != nil {
		t.Fatal("transformed bytes retained a location without evidence")
	}
	var plain document.Document
	if err := jsonv2.Unmarshal([]byte(`{"text":"old"}`), &plain); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"text":"changed","provenance":{"unknown":"invented"}}`, `{"text":"changed","provenance":{"source":"unversioned"}}`} {
		if err := jsonv2.Unmarshal([]byte(raw), &plain); err == nil || plain.Text != "old" {
			t.Fatalf("invalid evidence changed document: %+v, %v", plain, err)
		}
	}
}
