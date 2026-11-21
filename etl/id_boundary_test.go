package etl_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

type boundaryIDGenerator func(*document.Document) (string, error)

func (b boundaryIDGenerator) Generate(doc *document.Document) (string, error) {
	return b(doc)
}

func TestGeneratedIdentityUsesCanonicalDocumentValidation(t *testing.T) {
	for _, id := range []string{"\xff", "\xfe", "文档\x00/identity"} {
		t.Run(id, func(t *testing.T) {
			generator := boundaryIDGenerator(func(*document.Document) (string, error) { return id, nil })
			assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{Generator: generator})
			if err != nil {
				t.Fatal(err)
			}
			input := []*document.Document{{Text: "source"}}
			output, operationErr := assigner.Assign(input)
			if id == "文档\x00/identity" {
				if operationErr != nil || len(output) != 1 || output[0].ID != id {
					t.Fatalf("valid Core identity rejected: output=%v error=%v", output, operationErr)
				}
			} else if !errors.Is(operationErr, document.ErrInvalidDocument) || output != nil {
				t.Fatalf("invalid generated identity accepted: output=%v error=%v", output, operationErr)
			}
			if !reflect.DeepEqual(input, []*document.Document{{Text: "source"}}) {
				t.Fatalf("caller input mutated: %v", input)
			}
		})
	}
}

func TestGeneratedIdentityFailureDoesNotPublishPrefixes(t *testing.T) {
	calls := 0
	generator := boundaryIDGenerator(func(*document.Document) (string, error) {
		calls++
		if calls == 2 {
			return "\xff", nil
		}
		return "good", nil
	})
	assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{Generator: generator})
	if err != nil {
		t.Fatal(err)
	}
	input := []*document.Document{{Text: "first"}, {Text: "second"}}
	original := make([]*document.Document, len(input))
	for index, doc := range input {
		original[index] = doc.Clone()
	}
	output, err := assigner.Assign(input)
	if !errors.Is(err, document.ErrInvalidDocument) || output != nil || calls != 2 {
		t.Fatalf("failed identity batch published: calls=%d output=%v error=%v", calls, output, err)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("failed identity batch mutated input: %v", input)
	}
}

func TestIDAssignerGrantsOnlyIdentityToGenerator(t *testing.T) {
	assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{Generator: boundaryIDGenerator(func(input *document.Document) (string, error) {
		input.Text = "foreign"
		return "generated", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	source, err := document.NewDocument("original", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := assigner.Assign([]*document.Document{source})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != "generated" || got[0].Text != "original" {
		t.Fatalf("ID generation rewrote document content: %q %q", got[0].ID, got[0].Text)
	}
}
