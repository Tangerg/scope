package etl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

func TestSplitter_ReplacesLineageWhenSplittingUnidentifiedChunks(t *testing.T) {
	splitter, err := etl.NewSplitter(etl.SplitterConfig{
		SplitFunc: func(_ context.Context, text string) ([]string, error) {
			return strings.Fields(text), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := document.NewDocument("one two", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent.ID = "original"
	chunks, err := splitter.Split(t.Context(), []*document.Document{parent})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("first split returned %d chunks, want 2", len(chunks))
	}
	for index, chunk := range chunks {
		if chunk.ID != "" {
			t.Fatalf("chunk %d has an unexpected ID %q", index, chunk.ID)
		}
	}
	resplit, err := splitter.Split(t.Context(), chunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(resplit) != 2 {
		t.Fatalf("second split returned %d chunks, want 2", len(resplit))
	}
	for index, chunk := range resplit {
		if chunk.Text != chunks[index].Text {
			t.Errorf("chunk %d text = %q, want %q", index, chunk.Text, chunks[index].Text)
		}
		if _, exists := chunk.Metadata[etl.MetadataKeyParentID]; exists {
			t.Errorf("chunk %d inherited its grandparent as its parent", index)
		}
		if value, found, decodeErr := chunk.Metadata.Decode[int](etl.MetadataKeyChunkIndex); decodeErr != nil || !found || value != 0 {
			t.Errorf("chunk %d index = %d, %t, %v, want 0", index, value, found, decodeErr)
		}
		if value, found, decodeErr := chunk.Metadata.Decode[int](etl.MetadataKeyChunkTotal); decodeErr != nil || !found || value != 1 {
			t.Errorf("chunk %d total = %d, %t, %v, want 1", index, value, found, decodeErr)
		}
		if value, found, decodeErr := chunks[index].Metadata.Decode[string](etl.MetadataKeyParentID); decodeErr != nil || !found || value != "original" {
			t.Errorf("source chunk %d parent changed: %q, %t, %v", index, value, found, decodeErr)
		}
	}
}

func TestSplitter_StampsChunkLineage(t *testing.T) {
	splitter, err := etl.NewSplitter(etl.SplitterConfig{
		SplitFunc: func(_ context.Context, text string) ([]string, error) {
			// Includes an empty chunk to verify it is dropped before
			// chunk_index / chunk_total are computed.
			return []string{"a", "", "b", "c"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	parent, _ := document.NewDocument("ignored", nil)
	parent.ID = "parent-1"
	_ = parent.Metadata.Set("source", "manual")

	chunks, err := splitter.Split(t.Context(), []*document.Document{parent})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("want 3 non-empty chunks, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		if value, ok, _ := chunk.Metadata.Decode[int](etl.MetadataKeyChunkIndex); !ok || value != i {
			t.Fatalf("chunk %d: chunk_index = %v", i, value)
		}
		if value, ok, _ := chunk.Metadata.Decode[int](etl.MetadataKeyChunkTotal); !ok || value != 3 {
			t.Fatalf("chunk %d: chunk_total = %v", i, value)
		}
		if value, ok, _ := chunk.Metadata.Decode[string](etl.MetadataKeyParentID); !ok || value != "parent-1" {
			t.Fatalf("chunk %d: parent id = %v", i, value)
		}
		if value, ok, _ := chunk.Metadata.Decode[string]("source"); !ok || value != "manual" {
			t.Fatalf("chunk %d: original metadata not carried through", i)
		}
	}
}

func TestSplitter_NoParentIDWhenSourceUnidentified(t *testing.T) {
	splitter, _ := etl.NewSplitter(etl.SplitterConfig{
		SplitFunc: func(_ context.Context, _ string) ([]string, error) {
			return []string{"x"}, nil
		},
	})

	parent, _ := document.NewDocument("body", nil) // ID stays ""
	chunks, _ := splitter.Split(t.Context(), []*document.Document{parent})

	if _, ok := chunks[0].Metadata[etl.MetadataKeyParentID]; ok {
		t.Fatal("parent_document_id must be absent when source has no id")
	}
}

func TestSplitter_AssignsChunkIDs(t *testing.T) {
	splitter, _ := etl.NewSplitter(etl.SplitterConfig{
		IDGenerator: etl.NewSHA256IDGenerator(nil),
		SplitFunc: func(_ context.Context, _ string) ([]string, error) {
			return []string{"x", "y"}, nil
		},
	})

	parent, _ := document.NewDocument("body", nil)
	chunks, _ := splitter.Split(t.Context(), []*document.Document{parent})

	if chunks[0].ID == "" || chunks[1].ID == "" {
		t.Fatal("chunks must get ids when IDGenerator is set")
	}
	if chunks[0].ID == chunks[1].ID {
		t.Fatal("distinct chunks must get distinct ids")
	}
}
