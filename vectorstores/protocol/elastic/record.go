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

func (i indexMapping) admitRecordLayout() error {
	if i.Dynamic != dynamicMappingStrict || len(i.Properties) != 3 || i.Routing.Required || len(i.Runtime) > 0 || len(i.Derived) > 0 || len(i.DynamicTemplates) > 0 {
		return fmt.Errorf("%w: requires exactly the current strict properties without routing or dynamic rules", ErrIncompatibleIndex)
	}
	if i.Source.Enabled != nil && !*i.Source.Enabled || len(i.Source.Includes) > 0 || len(i.Source.Excludes) > 0 || i.Source.Mode != "" && i.Source.Mode != sourceModeStored {
		return fmt.Errorf("%w: requires complete stored _source", ErrIncompatibleIndex)
	}
	content, err := i.field(ContentField)
	if err != nil || content.Type != fieldTypeText {
		return fmt.Errorf("%w: content must be TEXT", ErrIncompatibleIndex)
	}
	metadata, err := i.field(MetadataField)
	if err != nil || metadata.Type != fieldTypeKeyword || metadata.Index == nil || *metadata.Index || metadata.DocValues == nil || *metadata.DocValues {
		return fmt.Errorf("%w: metadata_json must be an unindexed KEYWORD without doc values", ErrIncompatibleIndex)
	}
	if _, exists := i.Properties[EmbeddingField]; !exists {
		return fmt.Errorf("%w: requires an embedding field", ErrIncompatibleIndex)
	}
	return nil
}

func (i indexMapping) field(name string) (mappedField, error) {
	var field mappedField
	raw, exists := i.Properties[name]
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
