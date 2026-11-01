package storetest_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/inmemory"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestFilterConformance(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: queryFilterStore})
}

func TestFilterConformanceUnsupported(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{
		Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
			if strings.Contains(predicate.String(), " like ") {
				return nil, errors.ErrUnsupported
			}
			return queryFilterStore(ctx, docs, predicate)
		},
		Unsupported: map[string]error{
			"like_case": errors.ErrUnsupported, "like_whole_value": errors.ErrUnsupported,
			"like_percent": errors.ErrUnsupported, "like_unicode_rune": errors.ErrUnsupported,
			"like_literal_syntax": errors.ErrUnsupported, "like_literal_wildcards": errors.ErrUnsupported, "like_newline_rune": errors.ErrUnsupported,
		},
	})
}

func TestFilterConformanceRejectsIncorrectBackends(t *testing.T) {
	const probeEnvironment = "SCOPE_FILTER_CONFORMANCE_PROBE"
	if probe := os.Getenv(probeEnvironment); probe != "" {
		config := storetest.FilterConfig{Query: queryFilterStore}
		switch probe {
		case "widened_like", "duplicate_ids", "unexpected_error":
			config.Query = func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				if probe == "unexpected_error" {
					return nil, context.Canceled
				}
				if probe == "widened_like" && strings.Contains(predicate.String(), " like ") {
					var err error
					predicate, err = filter.Parse(`value like '%'`)
					if err != nil {
						return nil, err
					}
				}
				ids, err := queryFilterStore(ctx, docs, predicate)
				if probe == "duplicate_ids" && len(ids) > 0 {
					ids = append(ids, ids[0])
				}
				return ids, err
			}
		case "nil_query":
			config.Query = nil
		case "unknown_case":
			config.Unsupported = map[string]error{"unknown": errors.ErrUnsupported}
		case "nil_error":
			config.Unsupported = map[string]error{"like_case": (*os.PathError)(nil)}
		}
		if probe == "unexpected_error" || probe == "accepted_unsupported" {
			config.Unsupported = map[string]error{"like_case": errors.ErrUnsupported}
		}
		storetest.FilterConformance(t, config)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct{ name, diagnostic string }{
		{"widened_like", "backend query"},
		{"duplicate_ids", "backend query"},
		{"unexpected_error", "want no IDs and unsupported operation"},
		{"accepted_unsupported", "want no IDs and unsupported operation"},
		{"nil_query", "query is nil"},
		{"unknown_case", "invalid unsupported case"},
		{"nil_error", "invalid unsupported case"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestFilterConformanceRejectsIncorrectBackends$")
			command.Env = append(os.Environ(), probeEnvironment+"="+probe.name)
			output, err := command.CombinedOutput()
			exit, ok := errors.AsType[*exec.ExitError](err)
			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), probe.diagnostic) {
				t.Fatalf("probe %s = %v, output:\n%s", probe.name, err, output)
			}
		})
	}
}

func queryFilterStore(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
	response, err := queryMemoryDocuments(ctx, docs, predicate)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, result := range response.Results {
		ids = append(ids, result.Document.ID)
	}
	return ids, nil
}

func TestInMemoryProvenanceConformance(t *testing.T) {
	storetest.ProvenanceConformance(t, func(t *testing.T, doc *document.Document, predicate filter.Predicate) (*vectorstore.SearchResponse, error) {
		return queryMemoryDocuments(t.Context(), []*document.Document{doc}, predicate)
	})
}

func queryMemoryDocuments(ctx context.Context, docs []*document.Document, predicate filter.Predicate) (*vectorstore.SearchResponse, error) {
	const space vectorstore.SpaceID = "fixture"
	store, err := inmemory.NewStore(ctx, inmemory.StoreConfig{Space: space, Dimensions: 2})
	if err != nil {
		return nil, err
	}
	records := make([]vectorstore.Record, len(docs))
	for i, doc := range docs {
		records[i] = vectorstore.Record{Document: doc, Values: []float64{1, 0}}
	}
	if upsertErr := store.Upsert(ctx, &vectorstore.Batch{Space: space, Records: records}); upsertErr != nil {
		return nil, upsertErr
	}
	return store.Query(ctx, &vectorstore.Query{Space: space, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
}
