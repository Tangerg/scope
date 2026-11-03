package vectara

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/rag"
)

// Source metadata keys distinguish native document and passage provenance.
const (
	SourceDocumentIDMetadataKey = "vectara_document_id"
	SourceCorpusMetadataKey     = "vectara_corpus_key"
	PartMetadataKey             = "vectara_part_metadata"
)

type boundScope struct {
	ids        map[string]struct{}
	expression string
}

func bindScope(ids []string) (*boundScope, error) {
	if ids == nil {
		return nil, nil
	}
	scope := &boundScope{ids: make(map[string]struct{})}
	var literals []string
	size := utf8.RuneCountInString("doc.id IN ()")
	for _, id := range ids {
		if err := validID(id); err != nil {
			return nil, err
		}
		if _, ok := scope.ids[id]; ok {
			continue
		}
		// Vectara uses ANSI SQL string literals: an embedded single quote is doubled.
		literal := "'" + strings.ReplaceAll(id, "'", "''") + "'"
		if len(literals) > 0 {
			size += 2
		}
		size += utf8.RuneCountInString(literal)
		if size > nativeFilterMaxRunes {
			return nil, errors.New("vectara: document scope exceeds native filter budget")
		}
		scope.ids[id] = struct{}{}
		literals = append(literals, literal)
	}
	if len(literals) > 0 {
		scope.expression = "doc.id IN (" + strings.Join(literals, ", ") + ")"
	}
	return scope, nil
}

func validID(id string) error {
	if strings.TrimSpace(id) == "" || !utf8.ValidString(id) {
		return errors.New("vectara: invalid document ID")
	}
	return nil
}
func (b *boundScope) authorize(id string) error {
	if err := validID(id); err != nil {
		return err
	}
	if b != nil {
		if _, ok := b.ids[id]; !ok {
			return errors.New("vectara: document outside bound scope")
		}
	}
	return nil
}

type passage struct {
	DocumentID string         `json:"document_id"`
	Corpus     string         `json:"corpus_key"`
	Index      int            `json:"request_corpora_index"`
	Type       string         `json:"result_type"`
	Text       string         `json:"text"`
	Score      *rag.Score     `json:"score"`
	Metadata   jsontext.Value `json:"document_metadata"`
	Part       metadata.Map   `json:"part_metadata"`
}

func (p passage) candidate(corpus string) (rag.Candidate, error) {
	if p.Type != nativeTextResultType || p.Score == nil || p.Index != 0 || (p.Corpus != "" && p.Corpus != corpus) {
		return rag.Candidate{}, errors.New("vectara: invalid native passage or foreign corpus")
	}
	doc := &document.Document{Text: p.Text}
	if len(p.Metadata) != 0 {
		if err := doc.UnmarshalStoredMetadata(p.Metadata); err != nil {
			return rag.Candidate{}, err
		}
	}
	if err := sourceMetadata(doc.Metadata); err != nil {
		return rag.Candidate{}, err
	}
	// The managed service segments the submitted document. Its returned passage
	// cannot establish the submitted text's byte interval in the original.
	doc.Provenance = doc.Provenance.Excerpt()
	facts := doc.Metadata
	if facts == nil {
		facts = make(metadata.Map)
	}
	if err := facts.Set(SourceDocumentIDMetadataKey, p.DocumentID); err != nil {
		return rag.Candidate{}, err
	}
	if err := facts.Set(SourceCorpusMetadataKey, corpus); err != nil {
		return rag.Candidate{}, err
	}
	if p.Part != nil {
		if err := facts.Set(PartMetadataKey, p.Part); err != nil {
			return rag.Candidate{}, err
		}
	}
	// Part metadata and text identify evidence inside its source without admitting
	// query-relative scores or the arbitrary iteration order of JSON object keys.
	raw, err := jsonv2.Marshal([]any{corpus, p.DocumentID, doc.Provenance, p.Text, p.Part})
	if err != nil {
		return rag.Candidate{}, err
	}
	encoded := jsontext.Value(raw)
	if err = encoded.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return rag.Candidate{}, err
	}
	doc.ID = fmt.Sprintf("vectara:%x", sha256.Sum256(encoded))
	doc.Metadata = facts
	result := rag.Candidate{Document: doc, Score: *p.Score}
	return result, result.Validate()
}

func sourceMetadata(m metadata.Map) error {
	for _, key := range []string{SourceDocumentIDMetadataKey, SourceCorpusMetadataKey, PartMetadataKey} {
		if _, ok := m[key]; ok {
			return fmt.Errorf("vectara: reserved document metadata %q", key)
		}
	}
	return nil
}
