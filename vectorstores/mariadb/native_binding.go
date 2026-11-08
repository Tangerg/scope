package mariadb

import (
	"fmt"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	nativeTextCharset = "utf8mb4"
	nativeTinyText    = "tinytext"
	nativeText        = "text"
	nativeMediumText  = "mediumtext"
	nativeLongText    = "longtext"
)

type nativeBinding struct {
	dimensions int
	content    nativeTextColumn
	metadata   nativeTextColumn
}

func (n nativeBinding) encodeMetadata(doc *document.Document) ([]byte, error) {
	encoded, err := doc.MarshalStoredMetadata()
	if err != nil {
		return nil, err
	}
	if err := n.metadata.checkBytes(len(encoded)); err != nil {
		return nil, err
	}
	return encoded, nil
}

type nativeTextColumn struct {
	name     string
	dataType string
	charset  string
	maxBytes int64
}

func (n nativeTextColumn) validate() error {
	switch n.dataType {
	case nativeTinyText, nativeText, nativeMediumText, nativeLongText:
	default:
		return fmt.Errorf("mariadb: column %q must use a native TEXT type", n.name)
	}
	if n.charset != nativeTextCharset || n.maxBytes <= 0 {
		return fmt.Errorf("mariadb: column %q requires %s and a positive native byte capacity", n.name, nativeTextCharset)
	}
	return nil
}

func (n nativeTextColumn) checkBytes(size int) error {
	if int64(size) > n.maxBytes {
		return fmt.Errorf("%w: mariadb column %q requires %d bytes; native column permits %d", vectorstore.ErrInvalidDocument, n.name, size, n.maxBytes)
	}
	return nil
}
