package jsonschema_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

type refinedConstraintRecord struct {
	Name  string   `json:"name" jsonschema:"minLength=5,maxLength=8"`
	Count int      `json:"count" jsonschema:"minimum=5,maximum=8"`
	Items []string `json:"items" jsonschema:"minItems=2,maxItems=3"`
}

func (refinedConstraintRecord) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"name":  {MinLength: new(uint64(1)), MaxLength: new(uint64(10))},
		"count": {Minimum: new(float64(1)), Maximum: new(float64(10))},
		"items": {MinItems: new(uint64(1)), MaxItems: new(uint64(5))},
	}}
}

func TestForIntersectsStructConstraintsWithFieldRefinements(t *testing.T) {
	schema, err := jsonschema.For[refinedConstraintRecord]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"name":"a","count":5,"items":["a","b"]}`,
		`{"name":"123456789","count":5,"items":["a","b"]}`,
		`{"name":"valid","count":1,"items":["a","b"]}`,
		`{"name":"valid","count":9,"items":["a","b"]}`,
		`{"name":"valid","count":5,"items":["a"]}`,
		`{"name":"valid","count":5,"items":["a","b","c","d"]}`,
	} {
		if err := schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("field refinement was widened for %s: %v", raw, err)
		}
	}
	if err := schema.Validate([]byte(`{"name":"valid","count":5,"items":["a","b"]}`)); err != nil {
		t.Fatalf("intersection rejected a valid record: %v", err)
	}
}

type constraintChild struct {
	Name string `json:"name" jsonschema:"minLength=2"`
}

type constrainedReferences struct {
	Limited constraintChild `json:"limited"`
	Other   constraintChild `json:"other"`
}

func (constrainedReferences) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"limited": {Properties: map[string]jsonschema.Constraints{
			"name": {MinLength: new(uint64(5))},
		}},
	}}
}

func TestForScopesNestedConstraintsToTheirProperty(t *testing.T) {
	schema, err := jsonschema.For[constrainedReferences]()
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`{"limited":{"name":"valid"},"other":{"name":"ok"}}`)); err != nil {
		t.Fatalf("property constraint changed its sibling's shared type: %v", err)
	}
	for _, raw := range []string{
		`{"limited":{"name":"ok"},"other":{"name":"valid"}}`,
		`{"limited":{"name":"valid"},"other":{"name":"x"}}`,
	} {
		if err := schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("nested constraint lost for %s: %v", raw, err)
		}
	}
}

type nullableConstraintReferences struct {
	Limited *constraintChild `json:"limited" jsonschema:"nullable"`
	Other   *constraintChild `json:"other" jsonschema:"nullable"`
}

func (nullableConstraintReferences) JSONSchemaConstraints() jsonschema.Constraints {
	return constrainedReferences{}.JSONSchemaConstraints()
}

func TestForScopesNestedConstraintsThroughNullableProperties(t *testing.T) {
	schema, err := jsonschema.For[nullableConstraintReferences]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"limited":null,"other":null}`,
		`{"limited":{"name":"valid"},"other":{"name":"ok"}}`,
	} {
		if err := schema.Validate([]byte(raw)); err != nil {
			t.Errorf("nullable property or sibling was narrowed for %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"limited":{"name":"ok"},"other":null}`,
		`{"limited":null,"other":{"name":"x"}}`,
	} {
		if err := schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("nested constraint was lost for %s: %v", raw, err)
		}
	}
}

type sharedConstraintModel struct {
	Name string `json:"name" jsonschema:"minLength=2"`
}

type longConstraintOwner struct{}

func (longConstraintOwner) JSONSchemaModel() any { return sharedConstraintModel{} }

func (longConstraintOwner) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"name": {MinLength: new(uint64(5))},
	}}
}

type shortConstraintOwner struct{}

func (shortConstraintOwner) JSONSchemaModel() any { return sharedConstraintModel{} }

func (shortConstraintOwner) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"name": {MaxLength: new(uint64(3))},
	}}
}

func TestForKeepsModelerConstraintsWithTheirDomainOwner(t *testing.T) {
	schema, err := jsonschema.For[struct {
		Long  longConstraintOwner   `json:"long"`
		Short shortConstraintOwner  `json:"short"`
		Wire  sharedConstraintModel `json:"wire"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	if validateErr := schema.Validate([]byte(`{"long":{"name":"valid"},"short":{"name":"ok"},"wire":{"name":"ok"}}`)); validateErr != nil {
		t.Fatalf("one domain owner's constraint changed another model: %v", validateErr)
	}
	for _, raw := range []string{
		`{"long":{"name":"ok"},"short":{"name":"ok"},"wire":{"name":"ok"}}`,
		`{"long":{"name":"valid"},"short":{"name":"long"},"wire":{"name":"ok"}}`,
		`{"long":{"name":"valid"},"short":{"name":"ok"},"wire":{"name":"x"}}`,
	} {
		if validateErr := schema.Validate([]byte(raw)); !errors.Is(validateErr, jsonschema.ErrValidation) {
			t.Errorf("domain or wire constraint was lost for %s: %v", raw, validateErr)
		}
	}
	wire, err := jsonschema.For[sharedConstraintModel]()
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.Validate([]byte(`{"name":"ok"}`)); err != nil {
		t.Fatalf("shared wire prototype was changed: %v", err)
	}
}

type contradictoryConstraints struct {
	Name string `json:"name" jsonschema:"minLength=5"`
}

func (contradictoryConstraints) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"name": {MaxLength: new(uint64(3))},
	}}
}

func TestForDoesNotArbitrateContradictoryConstraints(t *testing.T) {
	schema, err := jsonschema.For[contradictoryConstraints]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"name":"a"}`, `{"name":"valid"}`} {
		if err := schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("conflicting constraints accepted %s: %v", raw, err)
		}
	}
}
