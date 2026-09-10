package chroma

import (
	"encoding/json"
	"testing"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
)

func TestMetadataNumbersUseLosslessSDKWireValues(t *testing.T) {
	t.Parallel()
	for _, sample := range []struct{ input, want string }{
		{`9007199254740993.0`, `9007199254740993`},
		{`9.007199254740993e15`, `9007199254740993`},
		{`0.1`, `0.100000000000000`},
		{`[9007199254740993.0,1]`, `[9007199254740993,1]`},
		{`[1,0.1,1e-16]`, `[1,0.1,1e-16]`},
	} {
		t.Run(sample.input, func(t *testing.T) {
			store := &Store{}
			opts, err := store.buildAddOptions([]*document.Document{{ID: "one", Text: "content", Metadata: metadata.Map{"value": json.RawMessage(sample.input)}}}, [][]float64{{1, 0}})
			if err != nil {
				t.Fatal(err)
			}
			operation, err := v2.NewCollectionAddOp(opts...)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(operation.Metadatas[0])
			if err != nil {
				t.Fatal(err)
			}
			var wire metadata.Map
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if got := string(wire["value"]); got != sample.want {
				t.Fatalf("wire number = %s, want %s", got, sample.want)
			}
		})
	}
}

func TestMetadataRejectsSDKNumberLoss(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`1.00000000000000001`, `1e-16`, `1e1000`, `[9007199254740993,0.1]`, `[1.00000000000000001]`} {
		t.Run(value, func(t *testing.T) {
			store := &Store{}
			_, err := store.buildAddOptions([]*document.Document{{ID: "one", Text: "content", Metadata: metadata.Map{"value": json.RawMessage(value)}}}, [][]float64{{1, 0}})
			if err == nil {
				t.Fatal("buildAddOptions accepted metadata that the SDK changes on the wire")
			}
		})
	}
}
