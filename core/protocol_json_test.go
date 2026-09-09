package core_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestProtocolJSONRejectsUnknownMembersWithoutChangingReceiver(t *testing.T) {
	imageMedia, err := media.NewBytes("image/png", []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	audioMedia, err := media.NewBytes("audio/wav", []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	message := chat.NewAssistantMessage(chat.NewTextPart("answer"))
	for _, test := range []struct {
		name  string
		value any
	}{
		{"chat request", &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}}},
		{"chat response", &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}},
		{"chat delta", &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("answer")}}},
		{"chat options", &chat.Options{Model: "audit"}},
		{"chat output format", &chat.OutputFormat{Type: chat.OutputFormatText}},
		{"document", &document.Document{ID: "doc", Text: "evidence"}},
		{"media", imageMedia},
		{"embedding request", &embedding.Request{Texts: []string{"text"}}},
		{"embedding response", &embedding.Response{Outputs: []*embedding.Output{{Embedding: []float64{1}}}}},
		{"image request", &image.Request{Prompt: "image"}},
		{"image response", &image.Response{Outputs: []*image.Output{{Media: imageMedia}}}},
		{"moderation request", &moderation.Request{Texts: []string{"text"}}},
		{"moderation response", &moderation.Response{Outputs: []*moderation.Output{{Categories: moderation.Categories{"provider/category": {Score: 0.5}}}}}},
		{"rerank request", &rerank.Request{Query: "query", Documents: []string{"text"}}},
		{"rerank response", &rerank.Response{Results: []*rerank.Result{{Index: 0, Score: 0.5}}}},
		{"speech request", &speech.Request{Text: "text"}},
		{"speech response", &speech.Response{Output: &speech.Output{Audio: []byte{1}}}},
		{"transcription request", &transcription.Request{Audio: audioMedia}},
		{"transcription response", &transcription.Response{Output: &transcription.Output{Text: "text"}}},
		{"index request", &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "doc", Text: "text"}}}},
		{"search request", &vectorstore.SearchRequest{Query: "query"}},
		{"search options", &vectorstore.SearchOptions{TopK: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, marshalErr := json.Marshal(test.value)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if decodeErr := json.Unmarshal(before, test.value); decodeErr != nil {
				t.Fatalf("valid round trip: %v", decodeErr)
			}
			unknown := []byte(string(before[:len(before)-1]) + `,"unexpected":true}`)
			if decodeErr := json.Unmarshal(unknown, test.value); !errors.Is(decodeErr, jsonv2.ErrUnknownName) {
				t.Fatalf("decode error = %v, want unknown object member", decodeErr)
			}
			after, marshalErr := json.Marshal(test.value)
			if marshalErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected decode changed receiver: before=%s after=%s error=%v", before, after, marshalErr)
			}
		})
	}
}

func TestProtocolJSONPreservesOpenPayloads(t *testing.T) {
	const opaque = `{"unexpected":{"large":9007199254740993,"nullable":null}}`
	var request chat.Request
	data := []byte(`{"messages":[{"role":"user","parts":[{"kind":"text","text":"question"}],"metadata":{"provider/fact":` + opaque + `}}],"options":{"extensions":{"provider/option":` + opaque + `}}}`)
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if got := string(request.Messages[0].Metadata["provider/fact"]); got != opaque {
		t.Fatalf("metadata = %s, want %s", got, opaque)
	}
	extension, present, err := request.Options.Extensions.Decode[json.RawMessage]("provider/option")
	if err != nil || !present || string(extension) != opaque {
		t.Fatalf("extension = %s, %t, %v", extension, present, err)
	}
	var output chat.ToolOutput
	if err := json.Unmarshal([]byte(`{"details":`+opaque+`}`), &output); err != nil {
		t.Fatal(err)
	}
	if got := string(output.Details); got != opaque {
		t.Fatalf("tool details = %s, want %s", got, opaque)
	}
}
