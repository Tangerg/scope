package couchbase

import (
	"encoding/json"
	"errors"
	"testing"
)

type rawQueryRow struct{ data []byte }

func (r rawQueryRow) Row(target any) error {
	raw, ok := target.(*json.RawMessage)
	if !ok {
		return errors.New("row must request raw JSON rather than a float64 map")
	}
	*raw = append((*raw)[:0], r.data...)
	return nil
}

func TestQueryRowPreservesExactMetadataNumbers(t *testing.T) {
	source := `{"id":"d","content":"text","metadata":{"safe":9007199254740992,"past_safe":9007199254740993,"negative":-9007199254740993,"max":9223372036854775807,"decimal":0.1234567890123456789,"nested":{"n":[9007199254740993] }},"_scope_score":0.75}`
	hit, err := decodeSearchRow(rawQueryRow{data: []byte(source)})
	if err != nil {
		t.Fatal(err)
	}
	values, err := hit.Document.Metadata.Values()
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"safe": "9007199254740992", "past_safe": "9007199254740993", "negative": "-9007199254740993", "max": "9223372036854775807", "decimal": "0.1234567890123456789"} {
		if got, ok := values[key].(json.Number); !ok || got.String() != want {
			t.Fatalf("%s = %v (%T), want %s", key, values[key], values[key], want)
		}
	}
	if got := values["nested"].(map[string]any)["n"].([]any)[0].(json.Number).String(); got != "9007199254740993" {
		t.Fatalf("nested %s", got)
	}
}
