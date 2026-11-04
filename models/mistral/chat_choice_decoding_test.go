package mistral

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestStreamChoiceDecodingIsAtomic(t *testing.T) {
	var choice chatCompletionStreamChoice
	if err := jsonv2.Unmarshal([]byte(`{"index":0,"delta":{"content":"original"},"finish_reason":"stop"}`), &choice); err != nil {
		t.Fatal(err)
	}
	before, err := jsonv2.Marshal(choice)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		data string
		want error
	}{
		{`null`, corechat.ErrInvalidResponse},
		{`{"delta":{"content":"changed"}}`, corechat.ErrInvalidResponse},
		{`{"index":null,"delta":{"content":"changed"}}`, corechat.ErrInvalidResponse},
		{`{"index":"0","delta":{"content":"changed"}}`, corechat.ErrInvalidResponse},
		{`{"index":0,"delta":{"role":false}}`, corechat.ErrInvalidResponse},
		{`{"index":0,"index":1,"delta":{"content":"changed"}}`, nil},
	} {
		if decodeErr := jsonv2.Unmarshal([]byte(sample.data), &choice); decodeErr == nil || sample.want != nil && !errors.Is(decodeErr, sample.want) {
			t.Fatalf("decode %s: error=%v want=%v", sample.data, decodeErr, sample.want)
		}
		after, encodeErr := jsonv2.Marshal(choice)
		if encodeErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("failed decode changed receiver: before=%s after=%s error=%v", before, after, encodeErr)
		}
	}
}

func TestStreamChoiceRejectsNilReceiver(t *testing.T) {
	var choice *chatCompletionStreamChoice
	if err := choice.UnmarshalJSON([]byte(`{"index":0}`)); !errors.Is(err, corechat.ErrInvalidResponse) {
		t.Fatalf("nil receiver error=%v", err)
	}
}
