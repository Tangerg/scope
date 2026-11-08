package jsonschema_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

type formattedBytes struct {
	Data []byte `json:"data,format:array"`
}

func TestForRejectsUnmodeledFormatTags(t *testing.T) {
	for name, derive := range map[string]func() (jsonschema.Schema, error){
		"byte slice": jsonschema.For[formattedBytes],
		"byte array": jsonschema.For[struct {
			Data [2]byte `json:"data,format:array"`
		}],
		"byte string": jsonschema.For[struct {
			Data []byte `json:"data,format:hex"`
		}],
		"time": jsonschema.For[struct {
			When time.Time `json:"when,format:DateOnly"`
		}],
		"duration": jsonschema.For[struct {
			Delay time.Duration `json:"delay,format:units"`
		}],
		"nested": jsonschema.For[map[string][]formattedBytes],
		"embedded": jsonschema.For[struct {
			formattedBytes
		}],
		"schema hidden": jsonschema.For[struct {
			Data []byte `json:"data,format:array" jsonschema:"-"`
		}],
		"hyphen name": jsonschema.For[struct {
			Data []byte `json:"'-',format:array"`
		}],
		"modeled member": jsonschema.For[struct {
			Value explicitlyEncodedText `json:"value,format:array"`
		}],
	} {
		t.Run(name, func(t *testing.T) {
			shape, err := derive()
			if shape.Valid() || !errors.Is(err, jsonschema.ErrInvalid) || !errors.Is(err, jsonschema.ErrUnsupportedFormatTag) {
				t.Fatalf("unmodeled field format published a schema: valid=%t, error=%v", shape.Valid(), err)
			}
		})
	}
}

func TestSignatureRejectsUnmodeledFieldFormats(t *testing.T) {
	if _, err := tool.NewSignature[formattedBytes, string](); !errors.Is(err, jsonschema.ErrUnsupportedFormatTag) {
		t.Fatalf("input format established a signature: %v", err)
	}
	if _, err := tool.NewSignature[struct{}, formattedBytes](); !errors.Is(err, jsonschema.ErrUnsupportedFormatTag) {
		t.Fatalf("output format established a signature: %v", err)
	}
}

type ownedDurationFormat struct {
	Delay time.Duration `json:"delay,format:units"`
}

func (o ownedDurationFormat) MarshalJSON() ([]byte, error) { return jsonv2.Marshal(o.Delay.String()) }

func (o *ownedDurationFormat) UnmarshalJSON(data []byte) error {
	var text string
	if err := jsonv2.Unmarshal(data, &text); err != nil {
		return err
	}
	delay, err := time.ParseDuration(text)
	if err != nil {
		return err
	}
	o.Delay = delay
	return nil
}

func (ownedDurationFormat) JSONSchemaModel() any { return "" }

func TestForRetainsAnEnclosingFormatOwner(t *testing.T) {
	value := ownedDurationFormat{Delay: time.Second}
	shape, err := jsonschema.For[ownedDurationFormat]()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jsonv2.Marshal(value)
	if err != nil || string(wire) != `"1s"` {
		t.Fatalf("owned duration wire=%s, error=%v", wire, err)
	}
	if err := shape.Validate(wire); err != nil {
		t.Fatalf("owned duration format rejected: %v", err)
	}
	var decoded ownedDurationFormat
	if err := jsonv2.Unmarshal(wire, &decoded); err != nil || decoded != value {
		t.Fatalf("owned duration did not round trip: %#v, %v", decoded, err)
	}
}

func TestFormatNamedPropertyRetainsItsOrdinaryWireShape(t *testing.T) {
	type input struct {
		Value string `json:"format:array"`
	}
	shape, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jsonv2.Marshal(input{Value: "value"})
	if err != nil {
		t.Fatal(err)
	}
	if err := shape.Validate(wire); err != nil {
		t.Fatalf("format-like property name was treated as a codec option: %s, %v", wire, err)
	}
}
