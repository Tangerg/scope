package bedrockkb

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestVectorSearchConfigKeepsRequestPolicyAuthoritative(t *testing.T) {
	t.Parallel()

	store := &Store{}
	config, err := store.vectorSearchConfig(&vectorstore.SearchRequest{
		Query:   "query",
		Options: vectorstore.SearchOptions{TopK: 7, Filter: filter.EQ("tenant", "one")},
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if config.NumberOfResults == nil || *config.NumberOfResults != 7 {
		t.Fatalf("NumberOfResults = %v, want 7", config.NumberOfResults)
	}
	if config.Filter == nil {
		t.Fatal("Filter = nil, want compiled request filter")
	}
	if config.OverrideSearchType != types.SearchTypeSemantic {
		t.Fatalf("OverrideSearchType = %q, want SEMANTIC", config.OverrideSearchType)
	}
}

func TestVectorSearchConfigMapsHybridMode(t *testing.T) {
	t.Parallel()

	store := &Store{}
	config, err := store.vectorSearchConfig(&vectorstore.SearchRequest{
		Query: "query", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid},
	}, vectorstore.DefaultTopK)
	if err != nil {
		t.Fatal(err)
	}
	if config.OverrideSearchType != types.SearchTypeHybrid {
		t.Fatalf("OverrideSearchType = %q, want HYBRID", config.OverrideSearchType)
	}
}

func TestVectorSearchConfigReturnsFilterCompilationError(t *testing.T) {
	t.Parallel()

	store := &Store{}
	_, err := store.vectorSearchConfig(&vectorstore.SearchRequest{
		Query:   "query",
		Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.Like("name", "%suffix")},
	}, 1)
	if err == nil {
		t.Fatal("vectorSearchConfig() error = nil, want unsupported suffix pattern")
	}
}
