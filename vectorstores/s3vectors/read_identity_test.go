package s3vectors

import (
	"errors"
	"testing"

	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestStoredIdentityMustSatisfyCoreIndexContract(t *testing.T) {
	raw := s3vdoc.NewLazyDocument(map[string]string{idMetaKey: " ", contentMetaKey: "text", metadataMetaKey: "null"})
	doc, err := decodeDocument(" ", raw)
	if doc != nil || !errors.Is(err, vectorstore.ErrMissingDocumentID) {
		t.Fatalf("stored identity = %#v, %v; want Core missing ID error", doc, err)
	}
}
