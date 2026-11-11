package vespa

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeRecord struct {
	document *document.Document
	metadata string
	vector   []float32
}

type nativeTensor struct {
	Type   string                          `json:"type,omitzero"`
	Values corevector.Coordinates[float64] `json:"values"`
}

var denseTensorType = regexp.MustCompile(`^tensor<float>\([A-Za-z_][A-Za-z0-9_]*\[([1-9][0-9]*)\]\)$`)

func encodeRecord(record vectorstore.Record, width int) (nativeRecord, error) {
	doc := record.Document
	if doc.Media != nil {
		return nativeRecord{}, vectorstore.ErrInvalidDocument
	}
	if err := validateText(doc.ID); err != nil {
		return nativeRecord{}, err
	}
	if err := validateText(doc.Text); err != nil {
		return nativeRecord{}, err
	}
	facts, err := doc.MarshalStoredMetadata()
	if err != nil {
		return nativeRecord{}, err
	}
	values, err := floatVector(record.Values, width)
	if err != nil {
		return nativeRecord{}, err
	}
	return nativeRecord{document: doc, metadata: escapeMetadataJSON(facts), vector: values}, nil
}

// Valid JSON syntax is ASCII, so every forbidden text character is inside a
// string. Escaping that inner JSON preserves metadata facts while preventing
// Vespa's string field validator from interpreting their raw code points.
func escapeMetadataJSON(raw []byte) string {
	var escaped strings.Builder
	start := 0
	for index, character := range string(raw) {
		if !isForbiddenTextCharacter(character) {
			continue
		}
		escaped.Write(raw[start:index])
		if character <= 0xffff {
			fmt.Fprintf(&escaped, `\u%04x`, character)
		} else {
			high, low := utf16.EncodeRune(character)
			fmt.Fprintf(&escaped, `\u%04x\u%04x`, high, low)
		}
		start = index + utf8.RuneLen(character)
	}
	if start == 0 {
		return string(raw)
	}
	escaped.Write(raw[start:])
	return escaped.String()
}

// Fields Vespa adds to a search summary beside the document's own fields.
const (
	summaryFieldDocumentName  = "sddocname"
	summaryFieldSummaryRank   = "summaryfeatures"
	summaryFieldMatchFeatures = "matchfeatures"
)

func decodeRecord(id string, fields metadata.Map, names schemaFields, summary bool) (nativeRecord, error) {
	if err := admitRecordFields(id, fields, names, summary); err != nil {
		return nativeRecord{}, err
	}
	text, present, err := fields.Decode[string](names.content)
	if err != nil || !present {
		return nativeRecord{}, fmt.Errorf("vespa: document %q lacks current content: %w", id, errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	facts, present, err := fields.Decode[string](metadataField)
	if err != nil || !present {
		return nativeRecord{}, fmt.Errorf("vespa: document %q lacks current metadata: %w", id, errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	doc := new(document.Document)
	if err = doc.UnmarshalStoredMetadata([]byte(facts)); err != nil {
		return nativeRecord{}, err
	}
	doc.ID, doc.Text = id, text
	if err = vectorstore.ValidateDocument(doc); err != nil {
		return nativeRecord{}, err
	}
	if err = validateText(id); err != nil {
		return nativeRecord{}, err
	}
	if err = validateText(text); err != nil {
		return nativeRecord{}, err
	}
	vector, err := decodeTensor(id, fields, names)
	if err != nil {
		return nativeRecord{}, err
	}
	return nativeRecord{document: doc, metadata: facts, vector: vector}, nil
}

// admitRecordFields allows only the document's current fields, plus the
// fields Vespa adds to a search summary.
func admitRecordFields(id string, fields metadata.Map, names schemaFields, summary bool) error {
	for name := range fields {
		switch name {
		case names.content, names.embedding, metadataField:
			continue
		case nativeIDField, summaryFieldDocumentName, summaryFieldSummaryRank, summaryFieldMatchFeatures:
			if summary {
				continue
			}
		}
		return fmt.Errorf("vespa: document %q has unexpected field %q", id, name)
	}
	return nil
}

// decodeTensor reads the one fixed float dimension a record's tensor declares
// and requires its values to fill it.
func decodeTensor(id string, fields metadata.Map, names schemaFields) ([]float32, error) {
	tensor, present, err := fields.Decode[nativeTensor](names.embedding)
	if err != nil || !present {
		return nil, fmt.Errorf("vespa: document %q lacks a dense tensor: %w", id, errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	dimensions := denseTensorType.FindStringSubmatch(tensor.Type)
	if dimensions == nil {
		return nil, fmt.Errorf("vespa: document %q tensor must be one fixed float dimension", id)
	}
	width, err := strconv.Atoi(dimensions[1])
	if err != nil || width != len(tensor.Values) {
		return nil, errors.New("vespa: tensor type and values disagree")
	}
	return floatVector(tensor.Values, width)
}

func floatVector(values []float64, width int) ([]float32, error) {
	if len(values) == 0 || (width != 0 && len(values) != width) {
		return nil, errors.New("vespa: vector has inconsistent dimensions")
	}
	vector := corevector.Float32(values)
	if err := corevector.Coordinates[float32](vector).Validate(); err != nil {
		return nil, err
	}
	for index, value := range values {
		narrowed := float64(vector[index])
		if value != 0 && narrowed == 0 {
			return nil, errors.New("vespa: vector is not representable in float32")
		}
	}
	return vector, nil
}

// Vespa's document IDs and string fields use com.yahoo.text.Text.isTextCharacter.
func validateText(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("vespa: text is not UTF-8")
	}
	for _, character := range value {
		if isForbiddenTextCharacter(character) {
			return errors.New("vespa: string contains a non-text character")
		}
	}
	return nil
}

func isForbiddenTextCharacter(character rune) bool {
	return (character < 0x20 && character != '\t' && character != '\n' && character != '\r') || (character >= 0xD800 && character <= 0xDFFF) || (character >= 0xFDD0 && character <= 0xFDDF) || character&0xffff >= 0xfffe
}

func quoteLiteral(value string) (string, error) {
	raw, err := jsonv2.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
