package speech_test

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
)

func TestDerivedSchemasMatchSpeechWireModels(t *testing.T) {
	request, err := speech.NewRequest("hello")
	if err != nil {
		t.Fatal(err)
	}
	request.Options.Model = "voice-model"
	requestSchema, err := jsonschema.For[speech.Request]()
	if err != nil {
		t.Fatal(err)
	}
	requestJSON, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if validationErr := requestSchema.Validate(requestJSON); validationErr != nil {
		t.Fatalf("request=%s schema=%s error=%v", requestJSON, requestSchema.JSON(), validationErr)
	}
	output, err := speech.NewOutput([]byte("audio"), metadata.Map{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := speech.NewResponse(output, &speech.ResponseMetadata{Model: "voice-model"})
	if err != nil {
		t.Fatal(err)
	}
	responseSchema, err := jsonschema.For[speech.Response]()
	if err != nil {
		t.Fatal(err)
	}
	responseJSON, err := jsonv2.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if validationErr := responseSchema.Validate(responseJSON); validationErr != nil {
		t.Fatalf("response=%s schema=%s error=%v", responseJSON, responseSchema.JSON(), validationErr)
	}
}
