package s3vectors

import (
	"encoding/json"
	"strings"
	"testing"

	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
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
