package text_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/eval/text"
)

func TestSamplesRejectUnencodableText(t *testing.T) {
	for _, sample := range []interface{ Validate() error }{
		text.AnswerRelevanceSample{Input: "\xff", Output: "output"},
		text.AnswerRelevanceSample{Input: "input", Output: "\xff"},
		text.GroundednessSample{Output: "\xff", Evidence: []string{"evidence"}},
		text.GroundednessSample{Output: "output", Evidence: []string{"evidence", "\xff"}},
		text.CorrectnessSample{Input: "\xff", Output: "output", Reference: "reference"},
		text.CorrectnessSample{Input: "input", Output: "\xff", Reference: "reference"},
		text.CorrectnessSample{Input: "input", Output: "output", Reference: "\xff"},
	} {
		if validationErr := sample.Validate(); !errors.Is(validationErr, text.ErrInvalidSample) {
			t.Fatalf("%+v Validate() = %v", sample, validationErr)
		}
	}
}

func TestSampleTextRemainsExact(t *testing.T) {
	value := "文本\x00é"
	sample := text.CorrectnessSample{Input: value, Output: value, Reference: value}
	if validationErr := sample.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	encoded, err := jsonv2.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	var decoded text.CorrectnessSample
	if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil || decoded != sample {
		t.Fatalf("sample round trip = %+v, %v", decoded, decodeErr)
	}
}
