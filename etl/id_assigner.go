package etl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
)

type IDAssignerConfig struct {
	Generator IDGenerator

	// Overwrite replaces existing IDs instead of preserving them.
	Overwrite bool
}

// IDAssigner assigns identifiers to independent copies of input documents.
// Caller-owned documents, metadata, and media remain untouched.
type IDAssigner struct {
	generator IDGenerator
	overwrite bool
}

func NewIDAssigner(config IDAssignerConfig) (*IDAssigner, error) {
	if lo.IsNil(config.Generator) {
		return nil, errors.New("etl: ID generator is required")
	}
	return &IDAssigner{
		generator: config.Generator,
		overwrite: config.Overwrite,
	}, nil
}

// Assign validates and clones every document before assigning IDs. It returns
// no partial output on failure and never mutates the input slice or documents.
// Generated identities must satisfy the canonical document contract.
func (i *IDAssigner) Assign(ctx context.Context, docs []*document.Document) ([]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owned := make([]*document.Document, len(docs))
	for index, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, fmt.Errorf("etl: assign ID to document %d: %w", index, ErrNilDocument)
		}
		if err := doc.Validate(); err != nil {
			return nil, fmt.Errorf("etl: assign ID to document %d: %w", index, err)
		}
		owned[index] = doc.Clone()
	}

	for index, doc := range owned {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if i.overwrite {
			doc.ID = ""
		} else if doc.ID != "" {
			continue
		}
		if err := assignID(ctx, doc, i.generator); err != nil {
			return nil, fmt.Errorf("etl: assign ID to document %d: %w", index, err)
		}
	}
	return owned, nil
}

func assignID(ctx context.Context, doc *document.Document, generator IDGenerator) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if doc == nil {
		return ErrNilDocument
	}
	if doc.ID != "" || generator == nil {
		return nil
	}
	generated, err := generator.Generate(ctx, doc)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if generated == "" || generated != strings.TrimSpace(generated) {
		return errors.New("etl: ID generator returned a blank or padded ID")
	}
	candidate := *doc
	candidate.ID = generated
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("etl: ID generator produced an invalid document: %w", err)
	}
	doc.ID = candidate.ID
	return nil
}
