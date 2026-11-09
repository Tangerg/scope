package jsonschema_test

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/speech"
)

type genericValue[T any] struct {
	Value T `json:"value"`
}

type genericNode[T any] struct {
	Value T               `json:"value"`
	Next  *genericNode[T] `json:"next,omitempty"`
}

func TestForResolvesNestedGenericDefinitions(t *testing.T) {
	type payload struct {
		Message genericValue[chat.Message] `json:"message"`
	}
	schema, err := jsonschema.For[payload]()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(payload{Message: genericValue[chat.Message]{Value: chat.NewUserMessage(chat.NewTextPart("question"))}})
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(encoded); err != nil {
		t.Fatalf("derived schema rejected nested generic wire %s: %v", encoded, err)
	}
	if err := schema.Validate([]byte(`{"message":{"value":17}}`)); err == nil {
		t.Fatal("nested generic schema lost its value constraint")
	}
}

func TestForKeepsGenericArgumentPackageIdentity(t *testing.T) {
	type payload struct {
		Chat   genericValue[chat.Output]   `json:"chat"`
		Speech genericValue[speech.Output] `json:"speech"`
	}
	schema, err := jsonschema.For[payload]()
	if err != nil {
		t.Fatal(err)
	}
	value := payload{
		Chat: genericValue[chat.Output]{Value: chat.Output{
			Message:      new(chat.NewAssistantMessage(chat.NewTextPart("answer"))),
			FinishReason: chat.FinishReasonStop,
		}},
		Speech: genericValue[speech.Output]{Value: speech.Output{Audio: []byte("audio")}},
	}
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(encoded); err != nil {
		t.Fatalf("derived schema conflated same-named generic arguments in %s: %v", encoded, err)
	}
}

func TestForResolvesRecursiveGenericDefinitions(t *testing.T) {
	schema, err := jsonschema.For[genericNode[chat.Message]]()
	if err != nil {
		t.Fatal(err)
	}
	value := genericNode[chat.Message]{
		Value: chat.NewUserMessage(chat.NewTextPart("question")),
		Next: &genericNode[chat.Message]{
			Value: chat.NewAssistantMessage(chat.NewTextPart("answer")),
		},
	}
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(encoded); err != nil {
		t.Fatalf("derived schema rejected recursive generic wire %s: %v", encoded, err)
	}
}
