package storetest_test

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type validatingCapabilities struct{}

func (validatingCapabilities) Upsert(_ context.Context, b *vectorstore.Batch) error {
	return b.ValidateFor("fixture")
}

func (validatingCapabilities) Query(_ context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor("fixture"); err != nil {
		return nil, err
	}
	return &vectorstore.SearchResponse{}, nil
}

func (validatingCapabilities) DeleteIDs(context.Context, []string) error { return nil }

func (validatingCapabilities) DeleteWhere(context.Context, filter.Predicate) error {
	return vectorstore.ErrMissingFilter
}

func TestRun(t *testing.T) {
	storetest.Run(t, validatingCapabilities{}, storetest.Capabilities{
		Upserter: true, Querier: true, IDDeleter: true, FilterDeleter: true,
	})
}

func TestVisitorConformanceAgreesWithCanonicalParser(t *testing.T) {
	// The canonical DSL round-trips through text, so supplying CompileText also
	// exercises the numeral-precision and segment-kind checks. Its numeric domain
	// is float64, matching the parser's literal formatting.
	storetest.VisitorConformance(t, func(source string) error {
		_, err := filter.Parse(source)
		return err
	}, storetest.Options{
		CompileText: func(source string) (string, error) {
			predicate, err := filter.Parse(source)
			if err != nil {
				return "", err
			}
			return predicate.String(), nil
		},
		NumericDomainIsFloat64: true,
	})
}
