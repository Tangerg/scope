package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/rag"
	ragtool "github.com/Tangerg/scope/rag/tool"
)

func TestRetrievalToolExposesStrictSchemaAndCandidates(t *testing.T) {
	doc := identifiedDocument(t, "go", "Go favors explicit composition.")
	var retrievedQuery string
	retriever := rag.RetrieverFunc(func(_ context.Context, query rag.Query) (rag.Candidates, error) {
		retrievedQuery = query.Text()
		return rag.Candidates{{Document: doc.Clone(), Score: 0.8}}, nil
	})
	retrievalTool, err := ragtool.NewRetrieval(ragtool.RetrievalConfig{
		Name:        "search_knowledge",
		Description: "Search the project knowledge base.",
		Retriever:   retriever,
	})
	if err != nil {
		t.Fatal(err)
	}
	var executable tool.Tool = retrievalTool
	definition := executable.Definition()
	if definition.Name != "search_knowledge" || !strings.Contains(string(definition.InputSchema), `"query"`) {
		t.Fatalf("definition = %#v", definition)
	}

	raw, err := invokeTestTool(t.Context(), executable, `{"query":"Go design"}`)
	if err != nil {
		t.Fatal(err)
	}
	var output ragtool.RetrievalOutput
	if err := json.Unmarshal(raw.Details, &output); err != nil {
		t.Fatal(err)
	}
	if err := output.Candidates.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(output.Candidates) != 1 || output.Candidates[0].Document.ID != "go" || output.Candidates[0].Score != 0.8 {
		t.Fatalf("output = %#v", output)
	}
	if retrievedQuery != "Go design" {
		t.Fatalf("retriever query = %q", retrievedQuery)
	}
}

func TestRetrievalToolRejectsInvalidConfigurationAndArguments(t *testing.T) {
	if _, err := ragtool.NewRetrieval(ragtool.RetrievalConfig{}); !errors.Is(err, rag.ErrNilRetriever) {
		t.Fatalf("nil retriever error = %v", err)
	}
	if _, err := ragtool.NewRetrieval(ragtool.RetrievalConfig{Retriever: rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) { return nil, nil })}); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("invalid definition error = %v", err)
	}

	retrievalTool, err := ragtool.NewRetrieval(ragtool.RetrievalConfig{
		Name: "search", Description: "Search evidence.", Retriever: rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) { return nil, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, arguments := range []string{
		`{}`,
		`{"query":""}`,
		`{"query":"valid","unexpected":true}`,
	} {
		if _, err := invokeTestTool(t.Context(), retrievalTool, arguments); err == nil {
			t.Fatalf("Call(%s) succeeded", arguments)
		}
	}
}

func TestRetrievalToolPreservesRetrieverErrors(t *testing.T) {
	want := errors.New("retrieval failed")
	retrievalTool, err := ragtool.NewRetrieval(ragtool.RetrievalConfig{
		Name:        "search",
		Description: "Search evidence.",
		Retriever: rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) {
			return nil, want
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invokeTestTool(t.Context(), retrievalTool, `{"query":"question"}`); !errors.Is(err, want) {
		t.Fatalf("retrieval error = %v", err)
	}
}

func TestRetrievalRejectsInvalidOrCanceledResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
		want   error
	}{
		{name: "invalid candidate", want: rag.ErrInvalidCandidate},
		{name: "canceled retrieval", cancel: true, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			retrieval, err := ragtool.NewRetrieval(ragtool.RetrievalConfig{
				Name: "search", Description: "Search evidence.",
				Retriever: rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) {
					if test.cancel {
						cancel()
						return nil, nil
					}
					return rag.Candidates{{}}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, callErr := invokeTestTool(ctx, retrieval, `{"query":"question"}`); !errors.Is(callErr, test.want) {
				t.Fatalf("Call error = %v, want %v", callErr, test.want)
			}
		})
	}
}

func identifiedDocument(t *testing.T, id, text string) *document.Document {
	t.Helper()
	doc, err := document.NewDocument(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc.ID = id
	return doc
}
