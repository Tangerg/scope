package jsonschema_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

type EmbeddedText struct {
	ID    string `json:"id"`
	Value string `json:"value,omitempty"`
}

type shallowFirst struct {
	ID int `json:"id"`
	EmbeddedText
}

type shallowLast struct {
	EmbeddedText
	ID int `json:"id"`
}

type untaggedName struct{ Value string }

type taggedName struct {
	Text int `json:"Value"`
}

type recursiveEmbedding struct {
	*recursiveEmbedding
	Value string `json:"value"`
}

func assertFieldLayout[T any](t *testing.T, value T, expected string) jsonschema.Schema {
	t.Helper()
	wire, err := jsonv2.Marshal(value)
	if err != nil || string(wire) != expected {
		t.Fatalf("JSON v2 wire=%s, want %s; error=%v", wire, expected, err)
	}
	shape, err := jsonschema.For[T]()
	if err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate(wire); err != nil {
		t.Fatalf("schema rejected codec output %s: %v; schema=%s", wire, err, shape.JSON())
	}
	return shape
}

func TestFieldLayoutFollowsJSONV2EmbeddingAndDominance(t *testing.T) {
	t.Run("explicit embedding", func(t *testing.T) {
		value := struct {
			Inner EmbeddedText `json:",embed"`
		}{Inner: EmbeddedText{ID: "chosen"}}
		shape := assertFieldLayout(t, value, `{"id":"chosen"}`)
		if err := shape.Validate([]byte(`{"Inner":{"id":"chosen"}}`)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Fatalf("schema admitted the replaced nested shape: %v", err)
		}
	})
	t.Run("shallow field before embedding", func(t *testing.T) {
		assertFieldLayout(t, shallowFirst{ID: 7, EmbeddedText: EmbeddedText{ID: "hidden"}}, `{"id":7}`)
	})
	t.Run("shallow field after embedding", func(t *testing.T) {
		assertFieldLayout(t, shallowLast{ID: 7, EmbeddedText: EmbeddedText{ID: "hidden"}}, `{"id":7}`)
	})
	t.Run("explicit name wins at equal depth", func(t *testing.T) {
		value := struct {
			untaggedName
			taggedName
		}{untaggedName: untaggedName{Value: "hidden"}, taggedName: taggedName{Text: 7}}
		assertFieldLayout(t, value, `{"Value":7}`)
	})
	t.Run("same Go names retain different JSON names", func(t *testing.T) {
		type left struct {
			Value int `json:"left"`
		}
		type right struct {
			Value string `json:"right"`
		}
		assertFieldLayout(t, struct {
			left
			right
		}{left{7}, right{"text"}}, `{"left":7,"right":"text"}`)
	})
	t.Run("recursive embedding", func(t *testing.T) {
		assertFieldLayout(t, recursiveEmbedding{recursiveEmbedding: &recursiveEmbedding{Value: "hidden"}, Value: "chosen"}, `{"value":"chosen"}`)
	})
	t.Run("named anonymous field", func(t *testing.T) {
		value := struct {
			EmbeddedText `json:"nested"`
		}{EmbeddedText{ID: "chosen"}}
		assertFieldLayout(t, value, `{"nested":{"id":"chosen"}}`)
	})
	t.Run("retired option has no embedding semantics", func(t *testing.T) {
		value := struct {
			Extra map[string]int `json:",inline"`
		}{Extra: map[string]int{"count": 7}}
		assertFieldLayout(t, value, `{"Extra":{"count":7}}`)
	})
}

func TestEmbeddedPointerFieldsCanBeAbsent(t *testing.T) {
	type input struct{ *EmbeddedText }
	shape := assertFieldLayout(t, input{}, `{}`)
	encoded, err := jsonv2.Marshal(input{EmbeddedText: &EmbeddedText{ID: "chosen"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"id":"chosen"}` {
		t.Fatalf("wire=%s", encoded)
	}
	if err := shape.Validate(encoded); err != nil {
		t.Fatal(err)
	}
	var decoded input
	if err := jsonv2.Unmarshal(encoded, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil || decoded.EmbeddedText == nil || decoded.ID != "chosen" {
		t.Fatalf("pointer embedding did not round trip: %#v, %v", decoded, err)
	}
}

func TestEmbeddedFieldsRetainOwnedSchemaTags(t *testing.T) {
	type bounded struct {
		Count int `json:"count" jsonschema:"minimum=2,maximum=4" jsonschema_description:"bounded count"`
	}
	type input struct {
		Value bounded `json:",embed"`
	}
	shape := assertFieldLayout(t, input{Value: bounded{Count: 3}}, `{"count":3}`)
	for _, wire := range []string{`{}`, `{"count":1}`, `{"count":5}`} {
		if err := shape.Validate([]byte(wire)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Fatalf("projected field lost its constraints for %s: %v", wire, err)
		}
	}
}

func TestEmbeddedToolContractAdmitsTheActualWire(t *testing.T) {
	type input struct {
		Value EmbeddedText `json:",embed"`
	}
	function, err := tool.NewFunc(tool.FuncConfig{Name: "embedded", Effect: tool.EffectSafe}, func(_ context.Context, value input) (string, error) { return value.Value.ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(function)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: "embedded", Arguments: `{"id":"chosen"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := binding.Call(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := result.Output.Text()
	if !ok || text != "chosen" {
		t.Fatalf("wrong tool output: %#v", result)
	}
	if _, err := binding.Contract().Prepare(tool.Request{Name: "embedded", Arguments: `{"Value":{"id":"chosen"}}`}, tool.Admission{}); err == nil {
		t.Fatal("tool admitted the obsolete nested layout")
	}
	var decoded input
	if err := jsonv2.Unmarshal([]byte(`{"id":"chosen"}`), &decoded); err != nil || !reflect.DeepEqual(decoded, input{Value: EmbeddedText{ID: "chosen"}}) {
		t.Fatalf("codec changed the projected value: %#v, %v", decoded, err)
	}
}
