package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/rag"
)

func TestAnswerResolvesToTheAdmittedRevision(t *testing.T) {
	r, err := run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if r.calls.documents != 2 || r.calls.queries != 1 {
		t.Fatalf("encodings: %+v", r.calls)
	}
	if r.ranked != 3 || r.citations.Len() != 2 || strings.Contains(r.input, "Oversized historical") || !strings.Contains(r.input, "Find your original receipt") {
		t.Fatalf("budget: %d fetched, %d admitted; model input=%q", r.ranked, r.citations.Len(), r.input)
	}
	if r.answer != policy+" [1]" || r.cited.Text != policy || r.cited.ID == "" {
		t.Fatalf("answer=%q cited=%+v", r.answer, r.cited)
	}
	p := r.cited.Provenance
	if p == nil || p.Source != policySource || p.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(policyFile))) || p.Bytes != nil || p.Page != 0 || len(p.Chunks) != 1 || p.Chunks[0].Parent == "" || p.Chunks[0].Index != 0 || p.Chunks[0].Total != 2 {
		t.Fatalf("wrong revision or invented position: %+v", p)
	}
	if d, resolveErr := r.citations.Resolve("[3]"); d != nil || !errors.Is(resolveErr, rag.ErrUnknownCitation) {
		t.Fatalf("omitted passage became citable: %+v, %v", d, resolveErr)
	}
}
