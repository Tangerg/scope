package pinecone

import (
	"encoding/json"
	"testing"

	pineconeclient "github.com/pinecone-io/go-pinecone/v4/pinecone"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestMetadataNumbersRoundTripThroughProtobuf(t *testing.T) {
	t.Parallel()
	values := metadata.Map{
		"number": json.RawMessage(`0.1`),
		"nested": json.RawMessage(`{"items":[1e-16,9007199254740992]}`),
	}
	store := &Store{distanceMetric: DistanceCosine}
	vectors, err := store.buildVectors([]*document.Document{{ID: "one", Text: "content", Metadata: values}}, [][]float64{{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(vectors[0].Metadata)
	if err != nil {
		t.Fatal(err)
	}
	var wire metadata.Map
	if decodeErr := json.Unmarshal(encoded, &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	delete(wire, payloadDocumentContentKey)
	if !wire.Equal(values) {
		t.Fatalf("wire metadata = %s, want %s", encoded, mustEncodeMetadata(t, values))
	}
	results, err := store.buildDocumentsFromScoredVectors([]*pineconeclient.ScoredVector{{Vector: vectors[0], Score: 1}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Document.Metadata.Equal(values) {
		t.Fatalf("round-trip results = %#v, want original numeric metadata", results)
	}
}

func mustEncodeMetadata(t *testing.T, values metadata.Map) []byte {
	t.Helper()
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestIndexRejectsMetadataNumberLossBeforeUpsert(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`9007199254740993`, `9007199254740993.0`, `9.007199254740993e15`, `1.00000000000000001`, `1e1000`, `{"items":[9007199254740993]}`} {
		t.Run(value, func(t *testing.T) {
			index := &countingIndex{upserted: 1}
			err := upsertStore(t, index).Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "content", Metadata: metadata.Map{"value": json.RawMessage(value)}}}})
			if err == nil {
				t.Fatal("Index accepted metadata that cannot be represented without loss")
			}
			if index.sent != 0 {
				t.Fatalf("sent %d vectors before rejecting metadata", index.sent)
			}
		})
	}
}
