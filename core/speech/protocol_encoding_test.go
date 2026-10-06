package speech_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/speech"
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
		{"speech.Model", func(text string) validatedProtocolValue { return speech.Options{Model: text} }, speech.ErrInvalidOptions},
		{"speech.Voice", func(text string) validatedProtocolValue { return speech.Options{Voice: text} }, speech.ErrInvalidOptions},
		{"speech.OutputFormat", func(text string) validatedProtocolValue { return speech.Options{OutputFormat: text} }, speech.ErrInvalidOptions},
		{"speech.Text", func(text string) validatedProtocolValue { return &speech.Request{Text: text} }, speech.ErrInvalidRequest},
		{"speech.Response.Model", func(text string) validatedProtocolValue {
			return &speech.Response{Output: &speech.Output{Audio: []byte{1}}, Metadata: &speech.ResponseMetadata{Model: text}}
		}, speech.ErrInvalidResponse},
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

func TestProtocolValidationRejectsUnrepresentableTimestamps(t *testing.T) {
	value := func(createdAt time.Time) validatedProtocolValue {
		return &speech.Response{Output: &speech.Output{Audio: []byte{1}}, Metadata: &speech.ResponseMetadata{CreatedAt: createdAt}}
	}
	for _, createdAt := range []time.Time{
		time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("invalid", 24*60*60)),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("seconds", 43)),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("negative seconds", -43)),
	} {
		invalid := value(createdAt)
		if err := invalid.Validate(); !errors.Is(err, speech.ErrInvalidResponse) {
			t.Errorf("Validate(%v) = %v; want %v", createdAt, err, speech.ErrInvalidResponse)
		}
		if _, err := jsonv2.Marshal(invalid); err == nil {
			t.Errorf("JSON encoding accepted invalid timestamp %v", createdAt)
		}
	}
	for _, createdAt := range []time.Time{
		{},
		time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("valid", 23*60*60)),
	} {
		valid := value(createdAt)
		if err := valid.Validate(); err != nil {
			t.Fatalf("valid timestamp %v rejected: %v", createdAt, err)
		}
		if _, err := jsonv2.Marshal(valid); err != nil {
			t.Fatalf("validated timestamp %v cannot be encoded: %v", createdAt, err)
		}
	}
}
