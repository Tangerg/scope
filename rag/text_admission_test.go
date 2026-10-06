package rag_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/rag"
)

func TestRetrievalTextRejectsInvalidUTF8Atomically(t *testing.T) {
	query, err := rag.NewQuery("\xff")
	if !errors.Is(err, rag.ErrInvalidQuery) || query.Text() != "" {
		t.Fatalf("NewQuery() = %q, %v", query.Text(), err)
	}
	original, err := rag.NewQuery("original")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := original.WithText("\xff")
	if !errors.Is(err, rag.ErrInvalidQuery) || updated.Text() != "" || original.Text() != "original" {
		t.Fatalf("WithText() = %q, %v", updated.Text(), err)
	}
	augmentation, err := rag.NewAugmentation("\xff")
	if !errors.Is(err, rag.ErrInvalidAugmentation) || augmentation.Text() != "" {
		t.Fatalf("NewAugmentation() = %q, %v", augmentation.Text(), err)
	}
}

func TestRetrievalTextPreservesValidUnicodeAndNUL(t *testing.T) {
	value := "资料\x00é"
	query, err := rag.NewQuery(value)
	if err != nil || query.Text() != value {
		t.Fatalf("NewQuery() = %q, %v", query.Text(), err)
	}
	augmentation, err := rag.NewAugmentation(value)
	if err != nil || augmentation.Text() != value {
		t.Fatalf("NewAugmentation() = %q, %v", augmentation.Text(), err)
	}
}
