package jsonschema_test

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestCoreProtocolsComposeWithSchema(t *testing.T) {
	cases := []struct {
		name   string
		derive func() (jsonschema.Schema, error)
	}{
		{"core/embedding/Options", jsonschema.For[embedding.Options]},
		{"core/embedding/Request", jsonschema.For[embedding.Request]},
		{"core/embedding/Output", jsonschema.For[embedding.Output]},
		{"core/embedding/Usage", jsonschema.For[embedding.Usage]},
		{"core/embedding/ResponseMetadata", jsonschema.For[embedding.ResponseMetadata]},
		{"core/embedding/Response", jsonschema.For[embedding.Response]},
		{"core/vectorstore/Record", jsonschema.For[vectorstore.Record]},
		{"core/vectorstore/Batch", jsonschema.For[vectorstore.Batch]},
		{"core/vectorstore/Query", jsonschema.For[vectorstore.Query]},
		{"core/vectorstore/Score", jsonschema.For[vectorstore.Score]},
		{"core/vectorstore/SearchOptions", jsonschema.For[vectorstore.SearchOptions]},
		{"core/vectorstore/SearchResult", jsonschema.For[vectorstore.SearchResult]},
		{"core/vectorstore/SearchResponse", jsonschema.For[vectorstore.SearchResponse]},
		{"core/rerank/Options", jsonschema.For[rerank.Options]},
		{"core/rerank/Request", jsonschema.For[rerank.Request]},
		{"core/rerank/Score", jsonschema.For[rerank.Score]},
		{"core/rerank/Result", jsonschema.For[rerank.Result]},
		{"core/rerank/Usage", jsonschema.For[rerank.Usage]},
		{"core/rerank/ResponseMetadata", jsonschema.For[rerank.ResponseMetadata]},
		{"core/rerank/Response", jsonschema.For[rerank.Response]},
		{"core/image/Options", jsonschema.For[image.Options]},
		{"core/image/Request", jsonschema.For[image.Request]},
		{"core/image/Output", jsonschema.For[image.Output]},
		{"core/image/ResponseMetadata", jsonschema.For[image.ResponseMetadata]},
		{"core/image/Response", jsonschema.For[image.Response]},
		{"core/transcription/Options", jsonschema.For[transcription.Options]},
		{"core/transcription/Request", jsonschema.For[transcription.Request]},
		{"core/transcription/Output", jsonschema.For[transcription.Output]},
		{"core/transcription/ResponseMetadata", jsonschema.For[transcription.ResponseMetadata]},
		{"core/transcription/Response", jsonschema.For[transcription.Response]},
		{"core/vector/Coordinates", jsonschema.For[vector.Coordinates[float64]]},
		{"core/history/Batch", jsonschema.For[history.Batch]},
		{"core/moderation/Options", jsonschema.For[moderation.Options]},
		{"core/moderation/Request", jsonschema.For[moderation.Request]},
		{"core/moderation/Verdict", jsonschema.For[moderation.Verdict]},
		{"core/moderation/Categories", jsonschema.For[moderation.Categories]},
		{"core/moderation/Output", jsonschema.For[moderation.Output]},
		{"core/moderation/ResponseMetadata", jsonschema.For[moderation.ResponseMetadata]},
		{"core/moderation/Response", jsonschema.For[moderation.Response]},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.derive()
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCoreImageProtocolFormsToolSignature(t *testing.T) {
	_, err := tool.NewSignature[image.Request, image.Response]()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCoreProtocolSchemasAcceptActualNestedWireValues(t *testing.T) {
	imageData, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := history.NewBatch("conversation", "append", chat.NewAssistantMessage(chat.NewTextPart("answer")))
	if err != nil {
		t.Fatal(err)
	}
	assertProtocolWire(t, embedding.Request{Texts: []string{"input"}})
	assertProtocolWire(t, embedding.Response{Outputs: []*embedding.Output{{Embedding: []float64{1, 0}}}, Metadata: &embedding.ResponseMetadata{Usage: &embedding.Usage{}}})
	assertProtocolWire(t, rerank.Request{Query: "query", Documents: []string{"document"}})
	assertProtocolWire(t, rerank.Response{Results: []*rerank.Result{{Index: 0, Score: 0}}})
	assertProtocolWire(t, image.Request{Prompt: "prompt"})
	assertProtocolWire(t, image.Response{Outputs: []*image.Output{{Media: imageData}}})
	assertProtocolWire(t, transcription.Response{Output: &transcription.Output{Text: ""}})
	assertProtocolWire(t, moderation.Response{Outputs: []*moderation.Output{{Categories: moderation.Categories{"category": {Flagged: false, Score: 0}}}}})
	assertProtocolWire(t, vectorstore.SearchOptions{})
	assertProtocolWire(t, vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}})
	assertProtocolWire(t, vector.Coordinates[float32]{1, 0})
	assertProtocolWire(t, batch)
}

func assertProtocolWire[T any](t *testing.T, value T) {
	t.Helper()
	type envelope struct {
		Value T `json:"value"`
	}
	shape, err := jsonschema.For[envelope]()
	if err != nil {
		t.Fatalf("%T schema: %v", value, err)
	}
	wire, err := jsonv2.Marshal(envelope{Value: value})
	if err != nil {
		t.Fatalf("%T wire: %v", value, err)
	}
	if err := shape.Validate(wire); err != nil {
		t.Fatalf("%T actual wire %s rejected: %v", value, wire, err)
	}
	var decoded envelope
	if err := jsonv2.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("%T round trip: %v", value, err)
	}
}
