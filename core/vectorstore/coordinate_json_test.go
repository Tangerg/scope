package vectorstore_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestVectorJSONRequiresEveryCoordinate(t *testing.T) {
	for _, values := range []string{`[null]`, `[1,null]`, `[null,0]`, `null`, `[]`} {
		t.Run(values, func(t *testing.T) {
			record := vectorstore.Record{Document: &document.Document{ID: "retained", Text: "text"}, Values: []float64{3, 4}}
			before := record
			err := jsonv2.Unmarshal([]byte(`{"document":{"id":"new","text":"text"},"values":`+values+`}`), &record)
			if !errors.Is(err, vectorstore.ErrInvalidRequest) || !reflect.DeepEqual(record, before) {
				t.Fatalf("record=%+v error=%v; invalid coordinates must not change the receiver", record, err)
			}
			query := vectorstore.Query{Space: "retained", Values: []float64{3, 4}}
			oldQuery := query
			err = jsonv2.Unmarshal([]byte(`{"space":"new","values":`+values+`,"options":{}}`), &query)
			if !errors.Is(err, vectorstore.ErrInvalidRequest) || !reflect.DeepEqual(query, oldQuery) {
				t.Fatalf("query=%+v error=%v; invalid coordinates must not change the receiver", query, err)
			}
			var batch vectorstore.Batch
			err = jsonv2.Unmarshal([]byte(`{"space":"new","records":[{"document":{"id":"doc","text":"text"},"values":`+values+`}]}`), &batch)
			if !errors.Is(err, vectorstore.ErrInvalidRequest) {
				t.Fatalf("batch admitted missing coordinates: %+v, %v", batch, err)
			}
		})
	}
	var query vectorstore.Query
	if err := jsonv2.Unmarshal([]byte(`{"space":"space","values":[0,-0.5,1],"options":{}}`), &query); err != nil || !reflect.DeepEqual(query.Values, []float64{0, -.5, 1}) {
		t.Fatalf("numeric coordinates=%v, error=%v", query.Values, err)
	}
}
