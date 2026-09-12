package eval

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// CaseID is a stable identity within one Dataset.
type CaseID string

func (c CaseID) String() string { return string(c) }

func (c CaseID) Validate() error {
	value := c.String()
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: id must be non-empty without surrounding whitespace", ErrInvalidCase)
	}
	return nil
}

// Case gives a stable identity to one evaluation subject. Subject is borrowed
// read-only: copying a Case does not copy objects referenced by T.
type Case[T any] struct {
	ID       CaseID
	Subject  T
	Metadata metadata.Map
}

func (c Case[T]) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if err := c.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidCase, err)
	}
	return nil
}

func (c Case[T]) clone() Case[T] {
	c.Metadata = c.Metadata.Clone()
	return c
}

// Dataset owns an ordered snapshot of case identities and metadata.
// FixtureID identifies the exact subjects and expectations, as assigned by the Host.
// Subjects remain borrowed read-only values; callers and evaluators must not mutate
// referenced objects while the Dataset is in use.
type Dataset[T any] struct {
	fixtureID string
	cases     []Case[T]
}

// NewDataset snapshots the case container and metadata, preserving Subject by
// assignment. It rejects duplicate identity before experiment scheduling can
// make result correlation ambiguous.
func NewDataset[T any](fixtureID string, cases ...Case[T]) (Dataset[T], error) {
	if fixtureID == "" || strings.TrimSpace(fixtureID) != fixtureID {
		return Dataset[T]{}, fmt.Errorf("%w: fixture identity is required", ErrInvalidDataset)
	}
	owned := slices.Clone(cases)
	seen := make(map[CaseID]struct{}, len(owned))
	for index, caseValue := range owned {
		if err := caseValue.Validate(); err != nil {
			return Dataset[T]{}, fmt.Errorf("%w: cases[%d]: %w", ErrInvalidDataset, index, err)
		}
		if _, exists := seen[caseValue.ID]; exists {
			return Dataset[T]{}, fmt.Errorf("%w: duplicate id %q", ErrInvalidDataset, caseValue.ID)
		}
		seen[caseValue.ID] = struct{}{}
		owned[index] = caseValue.clone()
	}
	return Dataset[T]{fixtureID: fixtureID, cases: owned}, nil
}

func (d Dataset[T]) FixtureID() string { return d.fixtureID }

func (d Dataset[T]) Len() int { return len(d.cases) }

// Cases copies the case container and metadata in declaration order. Subject
// is still borrowed read-only and can share referenced objects with the Dataset.
func (d Dataset[T]) Cases() []Case[T] {
	cases := slices.Clone(d.cases)
	for index := range cases {
		cases[index] = cases[index].clone()
	}
	return cases
}
