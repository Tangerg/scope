package vectorstore_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestHybridQueryRequiresEncodableLexicalText(t *testing.T) {
	q := &vectorstore.Query{Space: "space", Values: []float64{1}, LexicalText: "\xff", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}}
	if err := q.ValidateFor(q.Space); !errors.Is(err, vectorstore.ErrInvalidRequest) {
		t.Fatal(err)
	}
	if _, err := jsonv2.Marshal(q); !errors.Is(err, vectorstore.ErrInvalidRequest) {
		t.Fatal(err)
	}
	q.LexicalText = "value-é-值"
	if err := q.ValidateFor(q.Space); err != nil {
		t.Fatal(err)
	}
	if _, err := jsonv2.Marshal(q); err != nil {
		t.Fatal(err)
	}
}
