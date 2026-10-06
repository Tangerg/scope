package vectorstore_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

type validatedProtocolValue interface {
	Validate() error
}

func TestProtocolValidationRejectsUnencodableStrings(t *testing.T) {
	tests := []struct {
		name     string
		value    func(string) validatedProtocolValue
		category error
	}{
		{"vectorstore.Query", func(text string) validatedProtocolValue { return &vectorstore.SearchRequest{Query: text} }, vectorstore.ErrInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := test.value("\xff")
			if err := invalid.Validate(); err == nil || (test.category != nil && !errors.Is(err, test.category)) {
				t.Errorf("Validate() = %v; want rejection with %v", err, test.category)
			}
			if _, err := jsonv2.Marshal(invalid); err == nil {
				t.Error("JSON encoding accepted invalid UTF-8")
			}
			valid := test.value("value-é-值")
			if err := valid.Validate(); err != nil {
				t.Fatalf("valid Unicode rejected: %v", err)
			}
			if _, err := jsonv2.Marshal(valid); err != nil {
				t.Fatalf("validated value cannot be encoded: %v", err)
			}
		})
	}
}
