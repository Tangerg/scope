package qdrant

import (
	"encoding/json"
	"testing"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
)

func TestDocumentTextRoundTripsThroughReservedPayload(t *testing.T) {
	t.Parallel()

	store := &Store{distanceMetric: DistanceCosine}
	point, err := store.buildPointStruct(&document.Document{ID: "42", Text: "content"}, []float64{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	if got := point.Payload[payloadDocumentContentKey].GetStringValue(); got != "content" {
		t.Fatalf("stored content = %q, want content", got)
	}

	matches, err := store.buildDocumentsFromPoints([]*qdrantclient.ScoredPoint{{
		Id:      point.Id,
		Payload: point.Payload,
		Score:   1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Document.Text != "content" {
		t.Fatalf("matches = %#v, want one document with content", matches)
	}
	if _, leaked := matches[0].Document.Metadata[payloadDocumentContentKey]; leaked {
		t.Fatal("reserved content payload leaked into document metadata")
	}
}

func TestQueryResultRequiresStoredDocumentText(t *testing.T) {
	t.Parallel()

	store := &Store{distanceMetric: DistanceCosine}
	_, err := store.buildDocumentsFromPoints([]*qdrantclient.ScoredPoint{{Id: qdrantclient.NewIDNum(42)}})
	if err == nil {
		t.Fatal("buildDocumentsFromPoints() accepted a result without document text")
	}
}

func TestMetadataNumbersUseNumericPayloadValues(t *testing.T) {
	store := &Store{distanceMetric: DistanceCosine}
	point, err := store.buildPointStruct(&document.Document{ID: "42", Text: "content", Metadata: metadata.Map{
		"large":  json.RawMessage(`9007199254740993.0`),
		"nested": json.RawMessage(`{"items":[9.007199254740993e15,0.25]}`),
	}}, []float64{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	if got := point.Payload["large"].GetIntegerValue(); got != 9007199254740993 {
		t.Fatalf("large = %d", got)
	}
	items := point.Payload["nested"].GetStructValue().GetFields()["items"].GetListValue().GetValues()
	if len(items) != 2 || items[0].GetIntegerValue() != 9007199254740993 || items[1].GetDoubleValue() != 0.25 {
		t.Fatalf("items = %v", items)
	}
	for _, number := range []string{"1e1000", "1.00000000000000001"} {
		_, err := store.buildPointStruct(&document.Document{ID: "42", Text: "content", Metadata: metadata.Map{"number": json.RawMessage(number)}}, []float64{1, 0})
		if err == nil {
			t.Fatalf("accepted unrepresentable number %s", number)
		}
	}
}
