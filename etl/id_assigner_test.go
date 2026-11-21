package etl_test

import (
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

func TestIDAssigner_FillsEmptyOnly(t *testing.T) {
	assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{
		Generator: etl.UUIDGenerator{},
	})
	if err != nil {
		t.Fatal(err)
	}

	withID, _ := document.NewDocument("a", nil)
	withID.ID = "keep-me"
	withoutID, _ := document.NewDocument("b", nil)
	_ = withoutID.Metadata.Set("source", "original")

	out, err := assigner.Assign([]*document.Document{withID, withoutID})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].ID != "keep-me" {
		t.Fatalf("existing id must be preserved, got %q", out[0].ID)
	}
	if out[1].ID == "" {
		t.Fatal("empty id must be assigned")
	}
	if out[0] == withID || out[1] == withoutID {
		t.Fatal("Assign must return independent document copies")
	}
	if withoutID.ID != "" {
		t.Fatalf("Assign mutated caller-owned document ID: %q", withoutID.ID)
	}
	_ = out[1].Metadata.Set("source", "changed")
	if source, _, _ := withoutID.Metadata.Decode[string]("source"); source != "original" {
		t.Fatalf("output metadata aliases caller-owned metadata: %q", source)
	}
}

func TestIDAssigner_Overwrite(t *testing.T) {
	assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{
		Generator: etl.UUIDGenerator{},
		Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	doc, _ := document.NewDocument("a", nil)
	doc.ID = "old"
	out, _ := assigner.Assign([]*document.Document{doc})
	if out[0].ID == "old" || out[0].ID == "" {
		t.Fatalf("Overwrite must replace id, got %q", out[0].ID)
	}
	if doc.ID != "old" {
		t.Fatalf("Overwrite mutated caller-owned document ID: %q", doc.ID)
	}
}

func TestIDAssigner_RequiresGenerator(t *testing.T) {
	if _, err := etl.NewIDAssigner(etl.IDAssignerConfig{}); err == nil {
		t.Fatal("missing generator must error")
	}
}
