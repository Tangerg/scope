package jsonschema

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"strconv"
	"testing"
)

type LayoutMember struct {
	Value string `json:"value"`
}

type layoutPointer *LayoutMember

func schemaFromTestType(t *testing.T, typeOf reflect.Type) Schema {
	t.Helper()
	definition, err := reflectType(typeOf)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jsonv2.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	shape, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return shape
}

func TestInvalidJSONFieldLayoutsFailBeforeDerivation(t *testing.T) {
	for _, tag := range []string{
		"value,case:bogus", "value,case:ignore,case:strict", "value,omitEmpty",
		"value,omitzero,omitzero", "value,", "value,,omitempty", "value,case",
		"value,'omitzero'", "value,case:'ignore'", "'with,comma'", `with\escape`,
	} {
		t.Run(tag, func(t *testing.T) {
			typeOf := reflect.StructOf([]reflect.StructField{{
				Name: "Value", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(`json:` + strconv.Quote(tag)),
			}})
			if err := jsonv2.Unmarshal([]byte(`{}`), reflect.New(typeOf).Interface()); err == nil {
				t.Fatal("fixture is not an invalid JSON v2 layout")
			}
			if _, err := reflectType(typeOf); !errors.Is(err, ErrUnsupportedFieldLayout) {
				t.Fatalf("invalid layout published a schema: %v", err)
			}
		})
	}
	for name, fields := range map[string][]reflect.StructField{
		"same struct duplicate": {
			{Name: "A", Type: reflect.TypeFor[int](), Tag: `json:"value"`},
			{Name: "B", Type: reflect.TypeFor[string](), Tag: `json:"value"`},
		},
		"private tagged":          {{Name: "value", PkgPath: "fixture", Type: reflect.TypeFor[string](), Tag: `json:"value"`}},
		"embedded name":           {{Name: "Inner", Type: reflect.TypeFor[LayoutMember](), Tag: `json:"inner,embed"`}},
		"embedded omission":       {{Name: "Inner", Type: reflect.TypeFor[LayoutMember](), Tag: `json:",embed,omitempty"`}},
		"named pointer embedding": {{Name: "Inner", Type: reflect.TypeFor[layoutPointer](), Tag: `json:",embed"`}},
	} {
		t.Run(name, func(t *testing.T) {
			typeOf := reflect.StructOf(fields)
			if err := jsonv2.Unmarshal([]byte(`{}`), reflect.New(typeOf).Interface()); err == nil {
				t.Fatal("fixture is not an invalid JSON v2 layout")
			}
			if _, err := reflectType(typeOf); !errors.Is(err, ErrUnsupportedFieldLayout) {
				t.Fatalf("invalid layout published a schema: %v", err)
			}
		})
	}
}

func TestUnsupportedJSONFieldLayoutsRequireAnExplicitSchema(t *testing.T) {
	for name, field := range map[string]reflect.StructField{
		"embedded map": {Name: "Extra", Type: reflect.TypeFor[map[string]int](), Tag: `json:",embed"`},
		"hyphen name":  {Name: "Value", Type: reflect.TypeFor[string](), Tag: `json:"-,omitzero"`},
	} {
		t.Run(name, func(t *testing.T) {
			typeOf := reflect.StructOf([]reflect.StructField{field})
			if err := jsonv2.Unmarshal([]byte(`{}`), reflect.New(typeOf).Interface()); err != nil {
				t.Fatalf("unsupported fixture is not valid JSON v2: %v", err)
			}
			if _, err := reflectType(typeOf); !errors.Is(err, ErrUnsupportedFieldLayout) {
				t.Fatalf("unsupported layout published a schema: %v", err)
			}
		})
	}
}

func TestEqualDepthFieldConflictMatchesTheCodec(t *testing.T) {
	left := reflect.StructOf([]reflect.StructField{{Name: "Text", Type: reflect.TypeFor[string](), Tag: `json:"value"`}})
	right := reflect.StructOf([]reflect.StructField{{Name: "Number", Type: reflect.TypeFor[int](), Tag: `json:"value"`}})
	typeOf := reflect.StructOf([]reflect.StructField{
		{Name: "Left", Type: left, Tag: `json:",embed"`},
		{Name: "Right", Type: right, Tag: `json:",embed"`},
	})
	wire, err := jsonv2.Marshal(reflect.Zero(typeOf).Interface())
	if err != nil || string(wire) != `{}` {
		t.Fatalf("JSON v2 wire = %s, %v", wire, err)
	}
	shape := schemaFromTestType(t, typeOf)
	if err := shape.Validate(wire); err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate([]byte(`{"value":1}`)); !errors.Is(err, ErrValidation) {
		t.Fatalf("ambiguous field acquired an owner: %v", err)
	}
}

func TestRepeatedEmbeddedTypesUseTheCodecTraversal(t *testing.T) {
	deep := reflect.StructOf([]reflect.StructField{{Name: "Value", Type: reflect.TypeFor[string](), Tag: `json:"value"`}})
	common := reflect.StructOf([]reflect.StructField{{Name: "Deep", Type: deep, Tag: `json:",embed"`}})
	left := reflect.StructOf([]reflect.StructField{{Name: "CommonLeft", Type: common, Tag: `json:",embed"`}})
	right := reflect.StructOf([]reflect.StructField{{Name: "CommonRight", Type: common, Tag: `json:",embed"`}})
	typeOf := reflect.StructOf([]reflect.StructField{
		{Name: "Left", Type: left, Tag: `json:",embed"`},
		{Name: "Right", Type: right, Tag: `json:",embed"`},
	})
	value := reflect.New(typeOf)
	value.Elem().FieldByIndex([]int{0, 0, 0, 0}).SetString("first")
	value.Elem().FieldByIndex([]int{1, 0, 0, 0}).SetString("second")
	wire, err := jsonv2.Marshal(value.Interface())
	if err != nil || string(wire) != `{"value":"first"}` {
		t.Fatalf("JSON v2 wire = %s, %v", wire, err)
	}
	shape := schemaFromTestType(t, typeOf)
	if err := shape.Validate(wire); err != nil {
		t.Fatalf("schema diverged from JSON v2: %v; schema = %s", err, shape.JSON())
	}
	decoded := reflect.New(typeOf)
	if err := jsonv2.Unmarshal(wire, decoded.Interface(), jsonv2.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	if decoded.Elem().FieldByIndex([]int{0, 0, 0, 0}).String() != "first" || decoded.Elem().FieldByIndex([]int{1, 0, 0, 0}).String() != "" {
		t.Fatal("the schema admitted a different field owner")
	}
}
