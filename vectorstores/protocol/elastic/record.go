package elastic

import (
	"cmp"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Record fields of the strict three-field mapping. A record's _source holds
// exactly these members, and its native _id is its sole identity.
const (
	ContentField   = "content"
	EmbeddingField = "embedding"
	MetadataField  = "metadata_json"
)

const (
	dynamicMappingStrict = "strict"
	sourceModeStored     = "stored"
	fieldTypeText        = "text"
	fieldTypeKeyword     = "keyword"
)

var (
	ErrIndexMissing      = errors.New("elastic: index not found")
	ErrIncompatibleIndex = errors.New("elastic: native index is incompatible")
)

// StoredDocument is the exact _source of one record. Metadata is one Core JSON
// string rather than native mapped terms, so every Core value keeps its meaning.
type StoredDocument struct {
	Content      string                          `json:"content"`
	Embedding    corevector.Coordinates[float32] `json:"embedding"`
	MetadataJSON string                          `json:"metadata_json"`
}

func decodeRecord(hit Hit, schema Schema) (*document.Document, error) {
	if err := hit.Validate(); err != nil {
		return nil, err
	}
	var record StoredDocument
	if err := jsonv2.Unmarshal(hit.Source, &record, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if err := schema.ValidateIndexedVector(record.Embedding); err != nil {
		return nil, err
	}
	doc := new(document.Document)
	if err := doc.UnmarshalStoredMetadata([]byte(record.MetadataJSON)); err != nil {
		return nil, err
	}
	doc.ID, doc.Text = hit.ID, record.Content
	if err := vectorstore.ValidateDocument(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// indexMapping is the engine-neutral part of an index mapping: the namespace
// declaration and the strict record layout both engines require. Each engine
// admits its own embedding field.
type indexMapping struct {
	Meta struct {
		Space string `json:"scope_vector_space"`
	} `json:"_meta"`
	Dynamic    string                    `json:"dynamic"`
	Properties map[string]jsontext.Value `json:"properties"`
	Source     struct {
		Enabled  *bool    `json:"enabled"`
		Mode     string   `json:"mode"`
		Includes []string `json:"includes"`
		Excludes []string `json:"excludes"`
	} `json:"_source"`
	Routing struct {
		Required bool `json:"required"`
	} `json:"_routing"`
	Runtime          map[string]any `json:"runtime"`
	Derived          map[string]any `json:"derived"`
	DynamicTemplates []any          `json:"dynamic_templates"`
}

type mappedField struct {
	Type      string `json:"type"`
	Index     *bool  `json:"index"`
	DocValues *bool  `json:"doc_values"`
}

func (m indexMapping) admitRecordLayout() error {
	if m.Dynamic != dynamicMappingStrict || len(m.Properties) != 3 || m.Routing.Required || len(m.Runtime) > 0 || len(m.Derived) > 0 || len(m.DynamicTemplates) > 0 {
		return fmt.Errorf("%w: requires exactly the current strict properties without routing or dynamic rules", ErrIncompatibleIndex)
	}
	if m.Source.Enabled != nil && !*m.Source.Enabled || len(m.Source.Includes) > 0 || len(m.Source.Excludes) > 0 || m.Source.Mode != "" && m.Source.Mode != sourceModeStored {
		return fmt.Errorf("%w: requires complete stored _source", ErrIncompatibleIndex)
	}
	content, err := m.field(ContentField)
	if err != nil || content.Type != fieldTypeText {
		return fmt.Errorf("%w: content must be TEXT", ErrIncompatibleIndex)
	}
	metadata, err := m.field(MetadataField)
	if err != nil || metadata.Type != fieldTypeKeyword || metadata.Index == nil || *metadata.Index || metadata.DocValues == nil || *metadata.DocValues {
		return fmt.Errorf("%w: metadata_json must be an unindexed KEYWORD without doc values", ErrIncompatibleIndex)
	}
	if _, exists := m.Properties[EmbeddingField]; !exists {
		return fmt.Errorf("%w: requires an embedding field", ErrIncompatibleIndex)
	}
	return nil
}

func (m indexMapping) field(name string) (mappedField, error) {
	var field mappedField
	raw, exists := m.Properties[name]
	if !exists {
		return field, fmt.Errorf("%w: missing %s", ErrIncompatibleIndex, name)
	}
	return field, jsonv2.Unmarshal(raw, &field)
}

// IndexSettings holds flat explicit settings and the defaults they override.
type IndexSettings struct {
	Settings map[string]string `json:"settings"`
	Defaults map[string]string `json:"defaults"`
}

func (i IndexSettings) Value(name string) string { return cmp.Or(i.Settings[name], i.Defaults[name]) }

func settingNames(names []string) string {
	return strings.Join(append([]string{maxResultWindowSetting}, names...), ",")
}
