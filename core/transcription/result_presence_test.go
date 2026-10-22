package transcription_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/transcription"
)

func TestResponseJSONRequiresReportedText(t *testing.T) {
	for _, output := range []string{`{}`, `{"text":null}`} {
		kept := &transcription.Output{Text: "retained transcript"}
		response := transcription.Response{Output: kept}
		if err := jsonv2.Unmarshal([]byte(`{"output":`+output+`}`), &response); !errors.Is(err, transcription.ErrInvalidResponse) {
			t.Fatalf("decode %s error = %v, want ErrInvalidResponse", output, err)
		}
		if response.Output != kept || kept.Text != "retained transcript" {
			t.Fatal("rejected text changed the retained response")
		}
	}
	var response transcription.Response
	data := `{"output":{"text":""}}`
	if err := jsonv2.Unmarshal([]byte(data), &response); err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(response)
	if err != nil || string(encoded) != data {
		t.Fatalf("round trip = %s, %v; want %s", encoded, err, data)
	}
}
