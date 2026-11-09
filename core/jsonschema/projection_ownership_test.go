package jsonschema_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func TestAnonymousObjectRefinementsStayWithTheirField(t *testing.T) {
	type input struct {
		First struct {
			Count int `json:"count"`
		} `json:"first" jsonschema:"nullable" jsonschema_description:"first field" jsonschema_extras:"scope=first"`
		Second struct {
			Count int `json:"count"`
		} `json:"second" jsonschema_description:"second field" jsonschema_extras:"scope=second"`
	}
	shape, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate([]byte(`{"first":null,"second":{"count":0}}`)); err != nil {
		t.Fatalf("the first field lost its explicit schema refinement: %v", err)
	}
	if err := shape.Validate([]byte(`{"first":{"count":0},"second":null}`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("the first field's refinement leaked to the second: %v", err)
	}
	var document struct {
		Properties map[string]struct {
			Description string `json:"description"`
			Scope       string `json:"scope"`
		} `json:"properties"`
	}
	if err := jsonv2.Unmarshal(shape.JSON(), &document); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		property := document.Properties[name]
		if property.Description != name+" field" || property.Scope != name {
			t.Fatalf("%s field metadata changed: %#v", name, property)
		}
	}
}

type embeddedConstraintOwner struct {
	Count int `json:"count"`
}

func (embeddedConstraintOwner) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"count": {Minimum: new(float64(5))},
	}}
}

type embeddedModelOwner struct {
	Count int `json:"count"`
}

func (embeddedModelOwner) JSONSchemaModel() any {
	return struct {
		Count int `json:"count"`
	}{}
}

func TestEmbeddingCannotEraseDomainSchemaOwnership(t *testing.T) {
	for name, derive := range map[string]func() (jsonschema.Schema, error){
		"constraints": jsonschema.For[struct {
			Value embeddedConstraintOwner `json:",embed"`
		}],
		"model": jsonschema.For[struct {
			Value embeddedModelOwner `json:",embed"`
		}],
	} {
		t.Run(name, func(t *testing.T) {
			shape, err := derive()
			if shape.Valid() || !errors.Is(err, jsonschema.ErrUnsupportedFieldLayout) {
				t.Fatalf("embedded domain rules were silently flattened: %s, %v", shape.JSON(), err)
			}
		})
	}
	shape, err := jsonschema.For[struct {
		Value embeddedConstraintOwner `json:"value"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate([]byte(`{"value":{"count":5}}`)); err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate([]byte(`{"value":{"count":1}}`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("a named field lost its domain-owned minimum: %v", err)
	}
}

func TestAnonymousObjectsRetainPromotedDomainConstraints(t *testing.T) {
	_, err := jsonschema.For[struct {
		embeddedConstraintOwner `json:"nested"`
	}]()
	if !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("an anonymous object silently ignored its invalid property constraint: %v", err)
	}
}

type promotedRootConstraint struct{}

func (promotedRootConstraint) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"value": {MinLength: new(uint64(3))},
	}}
}

func TestAnonymousRootPublishesItsOwnedConstraints(t *testing.T) {
	shape, err := jsonschema.For[struct {
		promotedRootConstraint `json:"-"`
		Value                  string `json:"value"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate([]byte(`{"value":"valid"}`)); err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate([]byte(`{"value":"no"}`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("the anonymous root lost its owned constraint: %v", err)
	}
}

type inspectionCodec struct{}

func (inspectionCodec) JSONSchemaModel() any {
	return struct {
		Value string `json:"value"`
	}{}
}

func (inspectionCodec) MarshalJSON() ([]byte, error) { panic("schema derivation called MarshalJSON") }
func (*inspectionCodec) UnmarshalJSON([]byte) error  { panic("schema derivation called UnmarshalJSON") }
func (inspectionCodec) IsZero() bool                 { panic("schema derivation called IsZero") }
func (inspectionCodec) Validate() error              { panic("schema derivation called Validate") }

type inspectionZero struct {
	Value int `json:"value"`
}

func (inspectionZero) IsZero() bool { panic("schema derivation called IsZero") }

func TestFieldInspectionNeverCallsDomainValueMethods(t *testing.T) {
	shape, err := jsonschema.For[struct {
		Codec  inspectionCodec   `json:"codec,omitzero"`
		Zero   inspectionZero    `json:"zero,omitzero"`
		Schema jsonschema.Schema `json:"schema"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate([]byte(`{"codec":{"value":"owned"},"zero":{"value":1},"schema":true}`)); err != nil {
		t.Fatal(err)
	}
}

type namedToolWire struct {
	Value string `json:"value" jsonschema:"pattern=^[a-z]+$"`
}

type pureNamedModel namedToolWire

func (pureNamedModel) JSONSchemaModel() any { return namedToolWire{} }

func (p pureNamedModel) MarshalJSON() ([]byte, error) { return jsonv2.Marshal(namedToolWire(p)) }

func (p *pureNamedModel) UnmarshalJSON(data []byte) error {
	return jsonv2.Unmarshal(data, (*namedToolWire)(p))
}

func TestModelerPublishesTheCompleteRootWireShape(t *testing.T) {
	shape := assertFieldLayout(t, pureNamedModel{Value: "good"}, `{"value":"good"}`)
	var root struct {
		Type                 string                     `json:"type"`
		Reference            string                     `json:"$ref"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := jsonv2.Unmarshal(shape.JSON(), &root); err != nil {
		t.Fatal(err)
	}
	if root.Type != "object" || root.Reference != "" || len(root.Properties) != 1 || root.Properties["value"] == nil || !slices.Equal(root.Required, []string{"value"}) || root.AdditionalProperties == nil || *root.AdditionalProperties {
		t.Fatalf("the root does not expose its complete closed object contract: %s", shape.JSON())
	}
}

type namedToolModel namedToolWire

func (namedToolModel) JSONSchemaModel() any { return namedToolWire{} }

func (namedToolModel) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"value": {MinLength: new(uint64(3))},
	}}
}

func (n namedToolModel) MarshalJSON() ([]byte, error) { return jsonv2.Marshal(namedToolWire(n)) }

func (n *namedToolModel) UnmarshalJSON(data []byte) error {
	return jsonv2.Unmarshal(data, (*namedToolWire)(n))
}

type anonymousWireToolModel namedToolWire

func (anonymousWireToolModel) JSONSchemaModel() any {
	return struct {
		promotedRootConstraint `json:"-"`
		namedToolWire
	}{}
}

func (anonymousWireToolModel) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"value": {MaxLength: new(uint64(4))},
	}}
}

func (a anonymousWireToolModel) MarshalJSON() ([]byte, error) {
	return jsonv2.Marshal(namedToolWire(a))
}

func (a *anonymousWireToolModel) UnmarshalJSON(data []byte) error {
	return jsonv2.Unmarshal(data, (*namedToolWire)(a))
}

type chainedToolModel namedToolModel

func (chainedToolModel) JSONSchemaModel() any { return namedToolModel{} }

func (chainedToolModel) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"value": {MaxLength: new(uint64(4))},
	}}
}

func (c chainedToolModel) MarshalJSON() ([]byte, error) { return jsonv2.Marshal(namedToolModel(c)) }

func (c *chainedToolModel) UnmarshalJSON(data []byte) error {
	return jsonv2.Unmarshal(data, (*namedToolModel)(c))
}

type anonymousChainToolModel chainedToolModel

func (anonymousChainToolModel) JSONSchemaModel() any { return struct{ chainedToolModel }{} }

func (anonymousChainToolModel) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"value": {MinLength: new(uint64(4))},
	}}
}

func (a anonymousChainToolModel) MarshalJSON() ([]byte, error) {
	return jsonv2.Marshal(chainedToolModel(a))
}

func (a *anonymousChainToolModel) UnmarshalJSON(data []byte) error {
	return jsonv2.Unmarshal(data, (*chainedToolModel)(a))
}

func assertModeledToolInput[T any](t *testing.T, call func(context.Context, T) (string, error)) {
	t.Helper()
	function, err := tool.NewFunc(tool.FuncConfig{Name: "modeled", Effect: tool.EffectSafe}, call)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(function)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: "modeled", Arguments: `{"value":"good"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := binding.Call(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := result.Output.Text(); !ok || value != "good" {
		t.Fatalf("model wire representation did not reach the tool: %#v", result)
	}
	for _, arguments := range []string{`{"value":"no"}`, `{"value":"large"}`, `{"value":"LOUD"}`, `{"value":"good","extra":true}`} {
		if _, err := binding.Contract().Prepare(tool.Request{Name: "modeled", Arguments: arguments}, tool.Admission{}); err == nil {
			t.Fatalf("model chain lost a constraint for %s", arguments)
		}
	}
}

func TestNamedModelChainsRemainUsableAsToolInputs(t *testing.T) {
	assertModeledToolInput(t, func(_ context.Context, input chainedToolModel) (string, error) {
		return input.Value, nil
	})
}

func TestModelerRetainsAnonymousWireConstraints(t *testing.T) {
	assertModeledToolInput(t, func(_ context.Context, input anonymousWireToolModel) (string, error) {
		return input.Value, nil
	})
}

func TestAnonymousModelChainsRetainEveryDomainConstraint(t *testing.T) {
	assertModeledToolInput(t, func(_ context.Context, input anonymousChainToolModel) (string, error) {
		return input.Value, nil
	})
	shape, err := jsonschema.For[anonymousChainToolModel]()
	if err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate([]byte(`{"value":"mid"}`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("the outermost owner lost its refinement around the anonymous wire model: %v", err)
	}
}
