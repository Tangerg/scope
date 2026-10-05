package vectara

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativePart struct {
	Text             string             `json:"text"`
	Context          string             `json:"context,omitempty"`
	TableID          string             `json:"table_id,omitempty"`
	ImageID          string             `json:"image_id,omitempty"`
	CustomDimensions map[string]float64 `json:"custom_dimensions,omitempty"`
}

type nativeDocument struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
	Parts    []nativePart      `json:"parts"`
	Tables   []any             `json:"tables"`
	Images   []any             `json:"images"`
}

type nativeIndexDocument struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Metadata map[string]string `json:"metadata"`
	Parts    []nativePart      `json:"document_parts"`
}

func encodeDocument(doc *document.Document) (nativeIndexDocument, error) {
	facts, err := doc.Metadata.MarshalJSON()
	if err != nil {
		return nativeIndexDocument{}, err
	}
	return nativeIndexDocument{ID: doc.ID, Type: nativeDocumentType, Metadata: map[string]string{metadataField: string(facts)}, Parts: []nativePart{{Text: doc.Text}}}, nil
}

func decodeDocument(id, text string, properties map[string]string) (*document.Document, error) {
	if len(properties) != 1 {
		return nil, errors.New("vectara: native metadata does not have the current shape")
	}
	var facts metadata.Map
	if err := facts.UnmarshalJSON([]byte(properties[metadataField])); err != nil {
		return nil, err
	}
	doc := &document.Document{ID: id, Text: text, Metadata: facts}
	if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}

func relevanceScore(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < nativeMinimumScore || raw > nativeMaximumScore {
		return 0, fmt.Errorf("vectara: relevance score %v is outside the documented [-1, 1] scale", raw)
	}
	return vectorstore.ScoreFromCosineSimilarity(raw), nil
}

func quoteSQLString(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

type identitySelection struct {
	ids        []string
	expression string
}

func selectIdentities(docs []*document.Document) ([]identitySelection, error) {
	var groups []identitySelection
	var ids, literals []string
	runes := utf8.RuneCountInString("doc.id IN ()")
	for _, doc := range docs {
		literal := quoteSQLString(doc.ID)
		width := utf8.RuneCountInString(literal)
		if width+utf8.RuneCountInString("doc.id IN ()") > nativeFilterMaxRunes {
			return nil, errors.New("vectara: document identity exceeds the native filter limit")
		}
		if len(ids) != 0 && (len(ids) == identityGroupSize || runes+width+2 > nativeFilterMaxRunes) {
			groups = append(groups, identitySelection{ids: ids, expression: "doc.id IN (" + strings.Join(literals, ", ") + ")"})
			ids, literals = nil, nil
			runes = utf8.RuneCountInString("doc.id IN ()")
		}
		if len(ids) != 0 {
			runes += 2
		}
		runes += width
		ids = append(ids, doc.ID)
		literals = append(literals, literal)
	}
	if len(ids) != 0 {
		groups = append(groups, identitySelection{ids: ids, expression: "doc.id IN (" + strings.Join(literals, ", ") + ")"})
	}
	return groups, nil
}

type rankedDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}
