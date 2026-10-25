package jsonschema_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

type scalarModelOwner string

func (scalarModelOwner) JSONSchemaModel() any { return "" }

func (scalarModelOwner) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{MinLength: new(uint64(3)), MaxLength: new(uint64(5))}
}

type modelChainOwner struct{}

func (modelChainOwner) JSONSchemaModel() any { return scalarModelOwner("") }

func (modelChainOwner) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{MaxLength: new(uint64(4))}
}

func TestForKeepsConstraintsAcrossScalarModelChains(t *testing.T) {
	schema, err := jsonschema.For[modelChainOwner]()
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`"valid"`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("outer owner constraint was dropped: %v", err)
	}
	if err := schema.Validate([]byte(`"no"`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("wire owner constraint was dropped: %v", err)
	}
	if err := schema.Validate([]byte(`"good"`)); err != nil {
		t.Fatalf("model chain rejected its intersection: %v", err)
	}
}

type recursiveConstraintOwner struct{}

type recursiveConstraintModel struct {
	Name string                    `json:"name" jsonschema:"minLength=2"`
	Next *recursiveConstraintOwner `json:"next,omitzero" jsonschema:"nullable"`
}

func (recursiveConstraintOwner) JSONSchemaModel() any { return recursiveConstraintModel{} }

func (recursiveConstraintOwner) JSONSchemaConstraints() jsonschema.Constraints {
	return longConstraintOwner{}.JSONSchemaConstraints()
}

func TestForRetainsDomainConstraintsAcrossRecursiveModels(t *testing.T) {
	schema, err := jsonschema.For[recursiveConstraintOwner]()
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`{"name":"valid","next":{"name":"valid","next":null}}`)); err != nil {
		t.Fatalf("recursive domain model rejected its wire shape: %v", err)
	}
	if err := schema.Validate([]byte(`{"name":"valid","next":{"name":"ok"}}`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("recursive child lost its domain constraint: %v", err)
	}
}

type recursiveArrayModel struct{}

func (recursiveArrayModel) JSONSchemaModel() any { return []recursiveArrayModel{} }

func TestForAcceptsRecursionThroughAnArrayShape(t *testing.T) {
	schema, err := jsonschema.For[recursiveArrayModel]()
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`[[],[[]]]`)); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`[1]`)); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("recursive array lost its element model: %v", err)
	}
}

type selfModel struct{}

func (selfModel) JSONSchemaModel() any { return selfModel{} }

type cyclicModelLeft struct{}

func (cyclicModelLeft) JSONSchemaModel() any { return cyclicModelRight{} }

type cyclicModelRight struct{}

func (cyclicModelRight) JSONSchemaModel() any { return cyclicModelLeft{} }

type typedNilModel struct{}

func (typedNilModel) JSONSchemaModel() any { return (*sharedConstraintModel)(nil) }

type pointerModelConstraint struct{}

func (pointerModelConstraint) JSONSchemaModel() any { return sharedConstraintModel{} }

func (*pointerModelConstraint) JSONSchemaConstraints() jsonschema.Constraints {
	return longConstraintOwner{}.JSONSchemaConstraints()
}

type replacedModelHook struct{}

func (replacedModelHook) JSONSchemaAlias() any { return sharedConstraintModel{} }

func TestForRejectsModelsWithoutAnOwnedWireShape(t *testing.T) {
	for name, derive := range map[string]func() (jsonschema.Schema, error){
		"self":                  jsonschema.For[selfModel],
		"cycle":                 jsonschema.For[cyclicModelLeft],
		"typed nil":             jsonschema.For[typedNilModel],
		"pointer constraint":    jsonschema.For[pointerModelConstraint],
		"replaced model method": jsonschema.For[replacedModelHook],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := derive(); !errors.Is(err, jsonschema.ErrInvalid) {
				t.Fatalf("invalid model established a schema: %v", err)
			}
		})
	}
}

var errModelProjection = errors.New("model projection failed")

type failingModelProjection struct{}

func (failingModelProjection) JSONSchemaModel() any { panic(errModelProjection) }

func TestForPreservesModelProjectionFailureCause(t *testing.T) {
	if _, err := jsonschema.For[failingModelProjection](); !errors.Is(err, jsonschema.ErrInvalid) || !errors.Is(err, errModelProjection) {
		t.Fatalf("model projection lost its cause: %v", err)
	}
}
