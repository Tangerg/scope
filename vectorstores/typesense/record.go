package typesense

import (
	"encoding/base64"
	"errors"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

type storedRecord struct {
	ID        *string    `json:"id"`
	Content   *string    `json:"content"`
	Metadata  *string    `json:"metadata"`
	Embedding *[]float32 `json:"embedding"`
}

func encodeKey(id string) (string, error) {
	if id == "" || !utf8.ValidString(id) {
		return "", errors.New("typesense: Core document ID must be nonempty valid UTF-8")
	}
	return base64.RawURLEncoding.EncodeToString([]byte(id)), nil
}

func decodeKey(key string) (string, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(key)
	if err != nil || len(raw) == 0 || !utf8.Valid(raw) || base64.RawURLEncoding.EncodeToString(raw) != key {
		return "", errors.New("typesense: native key is not the canonical Core ID encoding")
	}
	return string(raw), nil
}

func encodeDocument(doc *document.Document) (storedRecord, error) {
	if doc.Media != nil {
		return storedRecord{}, vectorstore.ErrInvalidDocument
	}
	key, err := encodeKey(doc.ID)
	if err != nil {
		return storedRecord{}, err
	}
	encoded, err := doc.Metadata.MarshalJSON()
	if err != nil {
		return storedRecord{}, err
	}
	facts := string(encoded)
	content := doc.Text
	return storedRecord{ID: &key, Content: &content, Metadata: &facts}, nil
}
