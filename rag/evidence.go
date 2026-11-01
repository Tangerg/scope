package rag

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
)

// Excerpt pairs an original retrieval candidate with the exact text rendered
// from it. Content may be a host-formatted projection rather than a byte range
// in the original. The candidate's document remains the provenance owner.
type Excerpt struct {
	Candidate Candidate
	Content   string
}

// Evidence is an ordered selection of complete excerpts. The same sequence
// renders the prompt block and binds its marker positions to original documents.
type Evidence []Excerpt

// Render returns the JSON block that Augment inserts verbatim. A host can pass
// it to its token counter when deciding whether the next whole excerpt fits.
func (e Evidence) Render() (string, error) {
	type item struct {
		Citation string `json:"citation"`
		ID       string `json:"id,omitempty"`
		Content  string `json:"content"`
	}
	rows := make([]item, len(e))
	for i, excerpt := range e {
		if err := excerpt.Candidate.Validate(); err != nil {
			return "", fmt.Errorf("%w: excerpt %d: %w", ErrInvalidAugmentation, i, err)
		}
		if strings.TrimSpace(excerpt.Content) == "" {
			return "", fmt.Errorf("%w: excerpt %d is blank", ErrInvalidAugmentation, i)
		}
		rows[i] = item{Citation: CitationMarker(i), ID: excerpt.Candidate.Document.ID, Content: excerpt.Content}
	}
	data, err := jsonv2.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("%w: render evidence: %w", ErrInvalidAugmentation, err)
	}
	return string(data), nil
}

// Augment inserts the selected evidence block between prefix and suffix and
// freezes exactly those citations. There is no separate marker-table input.
func (e Evidence) Augment(prefix, suffix string) (Augmentation, error) {
	block, err := e.Render()
	if err != nil {
		return Augmentation{}, err
	}
	a, err := NewAugmentation(prefix + block + suffix)
	if err != nil {
		return Augmentation{}, err
	}
	rows := make(Candidates, len(e))
	for i, x := range e {
		rows[i] = x.Candidate.Clone()
	}
	a.citations = Citations{selected: rows}
	return a, nil
}
