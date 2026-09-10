package s3vectors

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestDecodeVectorMetadataSeparatesOwnedContent(t *testing.T) {
	text, values, err := decodeVectorMetadata("doc-1", s3vdoc.NewLazyDocument(map[string]any{
		contentMetaKey: "hello",
		"tenant":       "acme",
	}))
	if err != nil {
		t.Fatalf("decodeVectorMetadata: %v", err)
	}
	if text != "hello" || len(values) != 1 || values["tenant"] != "acme" {
		t.Fatalf("text = %q, metadata = %#v", text, values)
	}
}

// Numbers stay json.Number so filter evaluation compares an integer beyond the
// exact float64 range against its true value.
func TestDecodeVectorMetadataKeepsExactNumbers(t *testing.T) {
	_, values, err := decodeVectorMetadata("doc-1", s3vdoc.NewLazyDocument(map[string]any{
		contentMetaKey: "hello",
		"ordinal":      int64(9007199254740993),
	}))
	if err != nil {
		t.Fatalf("decodeVectorMetadata: %v", err)
	}
	number, ok := values["ordinal"].(json.Number)
	if !ok {
		t.Fatalf("ordinal = %#v, want json.Number", values["ordinal"])
	}
	if number.String() != "9007199254740993" {
		t.Fatalf("ordinal = %s, want 9007199254740993", number)
	}
}

func TestDecodeVectorMetadataRejectsMalformedContent(t *testing.T) {
	_, _, err := decodeVectorMetadata("doc-1", s3vdoc.NewLazyDocument(map[string]any{contentMetaKey: 42}))
	if err == nil || !strings.Contains(err.Error(), "must be a non-empty string") {
		t.Fatalf("decodeVectorMetadata error = %v", err)
	}
}

func TestDecodeVectorMetadataRejectsMissingMetadata(t *testing.T) {
	_, _, err := decodeVectorMetadata("doc-1", nil)
	if err == nil || !strings.Contains(err.Error(), "vector doc-1 is missing metadata") {
		t.Fatalf("decodeVectorMetadata error = %v", err)
	}
}

type metadataWriteClient struct {
	VectorClient
	encoded []byte
}

func (m *metadataWriteClient) PutVectors(_ context.Context, input *s3vectors.PutVectorsInput, _ ...func(*s3vectors.Options)) (*s3vectors.PutVectorsOutput, error) {
	encoded, err := input.Vectors[0].Metadata.MarshalSmithyDocument()
	if err != nil {
		return nil, err
	}
	m.encoded = encoded
	return &s3vectors.PutVectorsOutput{}, nil
}

func TestIndexEncodesMetadataNumbersAsSmithyNumbers(t *testing.T) {
	client := &metadataWriteClient{}
	store := queryStore(t, client)
	store.documentBatcher = singleBatcher{}
	source := metadata.Map{
		"large":  json.RawMessage(`9007199254740993.0`),
		"nested": json.RawMessage(`{"items":[9.007199254740993e15,1e1000]}`),
	}
	err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text", Metadata: source}}})
	if err != nil {
		t.Fatal(err)
	}
	var encoded metadata.Map
	if decodeErr := json.Unmarshal(client.encoded, &encoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	content, found, err := encoded.Decode[string](contentMetaKey)
	if err != nil || !found || content != "text" {
		t.Fatalf("content = %q, %t, %v", content, found, err)
	}
	delete(encoded, contentMetaKey)
	if !encoded.Equal(source) {
		t.Fatalf("metadata = %s, want %s", encoded, source)
	}
}
