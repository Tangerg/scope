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
	augmentation, err := rag.NewAugmentation("\xff")
	text, replace := augmentation.ReplacementText()
	if !errors.Is(err, rag.ErrInvalidAugmentation) || replace || text != "" {
		t.Fatalf("NewAugmentation() = %q, %v, %v", text, replace, err)
	}
}

func TestRetrievalTextPreservesValidUnicodeAndNUL(t *testing.T) {
	value := "资料\x00é"
	query, err := rag.NewQuery(value)
	if err != nil || query.Text() != value {
		t.Fatalf("NewQuery() = %q, %v", query.Text(), err)
	}
	augmentation, err := rag.NewAugmentation(value)
	text, replace := augmentation.ReplacementText()
	if err != nil || !replace || text != value {
		t.Fatalf("NewAugmentation() = %q, %v, %v", text, replace, err)
	}
}

func TestAugmentationSeparatesPreservationFromReplacement(t *testing.T) {
	var preserved rag.Augmentation
	text, replace := preserved.ReplacementText()
	if replace || text != "" || preserved.Citations().Len() != 0 {
		t.Fatalf("zero augmentation = %q, %v", text, replace)
	}
	const original = "\n  indented question\n"
	replacement, err := rag.NewAugmentation(original)
	text, replace = replacement.ReplacementText()
	if err != nil || !replace || text != original {
		t.Fatalf("replacement = %q, %v, %v", text, replace, err)
	}
	if _, err := rag.NewAugmentation(" \n"); !errors.Is(err, rag.ErrInvalidAugmentation) {
		t.Fatalf("blank replacement error = %v", err)
	}
}
