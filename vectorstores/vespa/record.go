package vespa

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeRecord struct {
	document *document.Document
	metadata string
	vector   []float32
}

type nativeTensor struct {
	Type   string    `json:"type,omitzero"`
	Values []float64 `json:"values"`
}

var denseTensorType = regexp.MustCompile(`^tensor<float>\([A-Za-z_][A-Za-z0-9_]*\[([1-9][0-9]*)\]\)$`)

func decodeRecord(id string, fields metadata.Map, names schemaFields, summary bool) (nativeRecord, error) {
	for name := range fields {
		if name == names.content || name == names.embedding || name == metadataField {
			continue
		}
		if summary && (name == nativeIDField || name == "sddocname" || name == "summaryfeatures" || name == "matchfeatures") {
			continue
		}
		return nativeRecord{}, fmt.Errorf("vespa: document %q has unexpected field %q", id, name)
	}
	text, present, err := fields.Decode[string](names.content)
	if err != nil || !present {
		return nativeRecord{}, fmt.Errorf("vespa: document %q lacks current content: %w", id, errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	facts, present, err := fields.Decode[string](metadataField)
	if err != nil || !present {
		return nativeRecord{}, fmt.Errorf("vespa: document %q lacks current metadata: %w", id, errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	var meta metadata.Map
	if err = meta.UnmarshalJSON([]byte(facts)); err != nil {
		return nativeRecord{}, err
	}
	doc := &document.Document{ID: id, Text: text, Metadata: meta}
	if err = (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nativeRecord{}, err
	}
	if err = validateText(id); err != nil {
		return nativeRecord{}, err
	}
	if err = validateText(text); err != nil {
		return nativeRecord{}, err
	}
	tensor, present, err := fields.Decode[nativeTensor](names.embedding)
	if err != nil || !present {
		return nativeRecord{}, fmt.Errorf("vespa: document %q lacks a dense tensor: %w", id, errors.Join(err, vectorstore.ErrInvalidDocument))
	}
	dimensions := denseTensorType.FindStringSubmatch(tensor.Type)
	if dimensions == nil {
		return nativeRecord{}, fmt.Errorf("vespa: document %q tensor must be one fixed float dimension", id)
	}
	width, err := strconv.Atoi(dimensions[1])
	if err != nil || width != len(tensor.Values) {
		return nativeRecord{}, errors.New("vespa: tensor type and values disagree")
	}
	vector, err := floatVector(tensor.Values, width)
	if err != nil {
		return nativeRecord{}, err
	}
	return nativeRecord{document: doc, metadata: facts, vector: vector}, nil
}

func floatVector(values []float64, width int) ([]float32, error) {
	if len(values) == 0 || (width != 0 && len(values) != width) {
		return nil, errors.New("vespa: vector has inconsistent dimensions")
	}
	vector := embedding.Float32Vector(values)
	for index, value := range values {
		narrowed := float64(vector[index])
		if math.IsNaN(value) || math.IsInf(value, 0) || math.IsInf(narrowed, 0) || (value != 0 && narrowed == 0) {
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
		if (character < 0x20 && character != '\t' && character != '\n' && character != '\r') || (character >= 0xD800 && character <= 0xDFFF) || (character >= 0xFDD0 && character <= 0xFDDF) || character&0xffff >= 0xfffe {
			return errors.New("vespa: string contains a non-text character")
		}
	}
	return nil
}

func quoteLiteral(value string) (string, error) {
	raw, err := jsonv2.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
