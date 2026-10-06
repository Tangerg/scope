package etl_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/markdown"
)

type boundaryIDGenerator func(context.Context, *document.Document) (string, error)

func (b boundaryIDGenerator) Generate(ctx context.Context, doc *document.Document) (string, error) {
	return b(ctx, doc)
}

func TestGeneratedIdentityUsesCanonicalDocumentValidation(t *testing.T) {
	for _, id := range []string{"\xff", "\xfe", "文档\x00/identity"} {
		t.Run(id, func(t *testing.T) {
			generator := boundaryIDGenerator(func(context.Context, *document.Document) (string, error) { return id, nil })
			assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{Generator: generator})
			if err != nil {
				t.Fatal(err)
			}
			splitter, err := etl.NewTextSplitter(etl.TextSplitterConfig{IDGenerator: generator})
			if err != nil {
				t.Fatal(err)
			}
			tokens, err := etl.NewTokenSplitter(etl.TokenSplitterConfig{Tokenizer: runeTokenizer{}, IDGenerator: generator})
			if err != nil {
				t.Fatal(err)
			}
			md, err := markdown.NewSplitter(markdown.SplitterConfig{Tokenizer: runeTokenizer{}, IDGenerator: generator})
			if err != nil {
				t.Fatal(err)
			}
			input := []*document.Document{{Text: "source"}}
			for name, operation := range map[string]func(context.Context, []*document.Document) ([]*document.Document, error){"assign": assigner.Assign, "text_split": splitter.Split, "token_split": tokens.Split, "markdown_split": md.Split} {
				t.Run(name, func(t *testing.T) {
					output, operationErr := operation(t.Context(), input)
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
		})
	}
}

func TestGeneratedIdentityFailureDoesNotPublishPrefixes(t *testing.T) {
	for _, stage := range []string{"assign", "split"} {
		t.Run(stage, func(t *testing.T) {
			calls := 0
			generator := boundaryIDGenerator(func(context.Context, *document.Document) (string, error) {
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
			splitter, err := etl.NewTextSplitter(etl.TextSplitterConfig{IDGenerator: generator})
			if err != nil {
				t.Fatal(err)
			}
			operation := assigner.Assign
			input := []*document.Document{{Text: "first"}, {Text: "second"}}
			if stage == "split" {
				operation = splitter.Split
				input = []*document.Document{{Text: "first\nsecond"}}
			}
			original := make([]*document.Document, len(input))
			for index, doc := range input {
				original[index] = doc.Clone()
			}
			output, operationErr := operation(t.Context(), input)
			if !errors.Is(operationErr, document.ErrInvalidDocument) || output != nil || calls != 2 {
				t.Fatalf("failed identity batch published: calls=%d output=%v error=%v", calls, output, operationErr)
			}
			if !reflect.DeepEqual(input, original) {
				t.Fatalf("failed identity batch mutated input: %v", input)
			}
		})
	}
}

func TestGeneratedIdentityRejectsCancellationBeforePublication(t *testing.T) {
	for _, stage := range []string{"assign", "split"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			generator := boundaryIDGenerator(func(context.Context, *document.Document) (string, error) {
				cancel()
				return "generated", nil
			})
			assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{Generator: generator})
			if err != nil {
				t.Fatal(err)
			}
			splitter, err := etl.NewTextSplitter(etl.TextSplitterConfig{IDGenerator: generator})
			if err != nil {
				t.Fatal(err)
			}
			operation := assigner.Assign
			if stage == "split" {
				operation = splitter.Split
			}
			output, operationErr := operation(ctx, []*document.Document{{Text: "source"}})
			if !errors.Is(operationErr, context.Canceled) || output != nil {
				t.Fatalf("canceled identity published: output=%v error=%v", output, operationErr)
			}
		})
	}
}
