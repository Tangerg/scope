package storetest_test

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type allCapabilities struct{}

func (allCapabilities) Index(context.Context, *vectorstore.IndexRequest) error {
	return vectorstore.ErrInvalidDocument
}
func (allCapabilities) Search(_ context.Context, request *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	return &vectorstore.SearchResponse{}, nil
}
func (allCapabilities) DeleteIDs(context.Context, []string) error { return nil }
func (allCapabilities) DeleteWhere(context.Context, filter.Predicate) error {
	return vectorstore.ErrMissingFilter
}

func TestRun(t *testing.T) {
	storetest.Run(t, validatingCapabilities{}, storetest.Capabilities{
		Indexer: true, Searcher: true, IDDeleter: true, FilterDeleter: true,
	})
}

func TestVisitorConformanceAgreesWithCanonicalParser(t *testing.T) {
	storetest.VisitorConformance(t, func(source string) error {
		_, err := filter.Parse(source)
		return err
	})
}

type validatingCapabilities struct{ allCapabilities }

func (validatingCapabilities) Index(_ context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	for _, doc := range request.Documents {
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
	}
	return nil
}
