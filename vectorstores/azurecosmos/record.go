package azurecosmos

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

const maxItemBytes = 2 * 1024 * 1024

type itemRecord struct {
	ID           *string         `json:"id"`
	PartitionKey *string         `json:"partition_key"`
	Content      *string         `json:"content"`
	Metadata     *string         `json:"metadata"`
	Embedding    *[]float32      `json:"embedding"`
	ETag         *string         `json:"_etag,omitzero"`
	RID          json.RawMessage `json:"_rid,omitzero"`
	Self         json.RawMessage `json:"_self,omitzero"`
	Attachments  json.RawMessage `json:"_attachments,omitzero"`
	Timestamp    json.RawMessage `json:"_ts,omitzero"`
}

func (i itemRecord) marshal() ([]byte, error) {
	body, err := jsonv2.Marshal(i)
	if err != nil {
		return nil, err
	}
	if len(body) > maxItemBytes {
		return nil, errors.New("azurecosmos: item exceeds the native 2 MiB budget")
	}
	return body, nil
}

func validateID(id string) error {
	if !utf8.ValidString(id) || id == "" || len(id) > 1023 || strings.ContainsAny(id, "/\\?#") {
		return errors.New("azurecosmos: item ID is invalid for the native SDK")
	}
	return nil
}

func encodeDocument(doc *document.Document, partition string) (itemRecord, error) {
	if doc.Media != nil {
		return itemRecord{}, vectorstore.ErrInvalidDocument
	}
	if err := validateID(doc.ID); err != nil {
		return itemRecord{}, err
	}
	encoded, err := doc.Metadata.MarshalJSON()
	if err != nil {
		return itemRecord{}, err
	}
	facts := string(encoded)
	record := itemRecord{ID: &doc.ID, PartitionKey: &partition, Content: &doc.Text, Metadata: &facts}
	if _, err = record.marshal(); err != nil {
		return itemRecord{}, err
	}
	return record, nil
}
