package s3vectors

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"unicode/utf8"

	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	idMetaKey          = "scope_id"
	contentMetaKey     = "scope_content"
	metadataMetaKey    = "scope_metadata"
	maxMetadataBytes   = 40 * 1024
	maxFilterableBytes = 2 * 1024
)

func validateKey(id string) error {
	if !utf8.ValidString(id) || utf8.RuneCountInString(id) < 1 || utf8.RuneCountInString(id) > 1024 {
		return errors.New("s3vectors: key must contain 1 to 1024 valid UTF-8 characters")
	}
	return nil
}

func encodeDocument(doc *document.Document) (s3vdoc.Interface, error) {
	if doc.Media != nil {
		return nil, fmt.Errorf("s3vectors: %w: media is unsupported", vectorstore.ErrInvalidDocument)
	}
	if err := validateKey(doc.ID); err != nil {
		return nil, err
	}
	encoded, err := doc.Metadata.MarshalJSON()
	if err != nil {
		return nil, err
	}
	projection := s3vdoc.NewLazyDocument(map[string]string{idMetaKey: doc.ID})
	filterable, err := projection.MarshalSmithyDocument()
	if err != nil {
		return nil, err
	}
	if len(filterable) > maxFilterableBytes {
		return nil, errors.New("s3vectors: ID projection exceeds the native filterable metadata budget")
	}
	wire := s3vdoc.NewLazyDocument(map[string]string{idMetaKey: doc.ID, contentMetaKey: doc.Text, metadataMetaKey: string(encoded)})
	full, err := wire.MarshalSmithyDocument()
	if err != nil {
		return nil, err
	}
	if len(full) > maxMetadataBytes {
		return nil, errors.New("s3vectors: document exceeds the native metadata budget")
	}
	return wire, nil
}

func decodeDocument(key string, raw s3vdoc.Interface) (*document.Document, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	if lo.IsNil(raw) {
		return nil, errors.New("s3vectors: native vector has no metadata")
	}
	encoded, err := raw.MarshalSmithyDocument()
	if err != nil {
		return nil, err
	}
	var wire struct {
		ID       *string `json:"scope_id"`
		Content  *string `json:"scope_content"`
		Metadata *string `json:"scope_metadata"`
	}
	if err = jsonv2.Unmarshal(encoded, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if wire.ID == nil || *wire.ID != key || wire.Content == nil || wire.Metadata == nil {
		return nil, errors.New("s3vectors: native document does not match the strict current schema or key projection")
	}
	var facts metadata.Map
	if err = facts.UnmarshalJSON([]byte(*wire.Metadata)); err != nil {
		return nil, err
	}
	doc := &document.Document{ID: key, Text: *wire.Content, Metadata: facts}
	if err = (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}
