package elastic

import (
	"fmt"

	"github.com/Tangerg/scope/core/vectorstore"
)

const maximumDocumentIDBytes = 512

type DocumentID string

func (d DocumentID) Validate() error {
	if d == "" {
		return vectorstore.ErrMissingDocumentID
	}
	if len(d) > maximumDocumentIDBytes {
		return fmt.Errorf("elastic: document ID exceeds native %d-byte limit", maximumDocumentIDBytes)
	}
	return nil
}
