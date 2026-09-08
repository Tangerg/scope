package s3vectors

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

// pagingQueryClient answers QueryVectors from scripted pages and records the
// continuation token each call carried. Unused operations stay unimplemented so
// a call to one fails the test rather than passing silently.
type pagingQueryClient struct {
	VectorClient
	pages  [][]string
	tokens []*string
	calls  int
}

func (p *pagingQueryClient) QueryVectors(
	_ context.Context,
	input *s3vectors.QueryVectorsInput,
	_ ...func(*s3vectors.Options),
) (*s3vectors.QueryVectorsOutput, error) {
	p.tokens = append(p.tokens, input.NextToken)
	index := p.calls
	p.calls++
	if index >= len(p.pages) {
		return nil, fmt.Errorf("unexpected QueryVectors call %d", index+1)
	}

	output := &s3vectors.QueryVectorsOutput{}
	for _, key := range p.pages[index] {
		distance := float32(0.25)
		output.Vectors = append(output.Vectors, types.QueryOutputVector{
			Key:      aws.String(key),
			Distance: &distance,
			Metadata: s3vdoc.NewLazyDocument(map[string]any{contentMetaKey: "text of " + key}),
		})
	}
	if index+1 < len(p.pages) {
		output.NextToken = aws.String(fmt.Sprintf("token-%d", index+1))
	}
	return output, nil
}

func queryStore(t *testing.T, client VectorClient) *Store {
	t.Helper()
	embeddings, err := embeddingclient.New(embedding.ModelFunc(
		func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
			outputs := make([]*embedding.Output, len(request.Texts))
			for index := range outputs {
				outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
			}
			return embedding.NewResponse(outputs, nil)
		}))
	if err != nil {
		t.Fatal(err)
	}
	return &Store{
		vectorBucketName: "bucket",
		indexName:        "documents",
		client:           client,
		embeddingClient:  embeddings,
		distanceMetric:   DistanceCosine,
	}
}

// A QueryVectors response carries at most 100 hits plus a continuation token,
// so a TopK above that is answered across pages. Reading one page would return
// a short result set that ValidateFor cannot detect, because it only bounds the
// count from above.
func TestSearchFollowsQueryPagination(t *testing.T) {
	t.Parallel()

	client := &pagingQueryClient{pages: [][]string{{"a", "b"}, {"c"}}}
	response, err := queryStore(t, client).Search(t.Context(), &vectorstore.SearchRequest{
		Query:   "query",
		Options: vectorstore.SearchOptions{TopK: 3},
	})
	if err != nil {
		t.Fatalf("Search() = %v, want nil", err)
	}
	if len(response.Results) != 3 {
		t.Fatalf("results = %d, want 3", len(response.Results))
	}
	if client.calls != 2 {
		t.Fatalf("QueryVectors calls = %d, want 2", client.calls)
	}
	if client.tokens[0] != nil {
		t.Fatalf("first call NextToken = %v, want nil", *client.tokens[0])
	}
	if client.tokens[1] == nil || *client.tokens[1] != "token-1" {
		t.Fatalf("second call NextToken = %v, want token-1", client.tokens[1])
	}
}

// An absent token is the only evidence the ranked run is complete, and a
// complete run may hold fewer results than were requested.
func TestSearchStopsWhenNoTokenRemains(t *testing.T) {
	t.Parallel()

	client := &pagingQueryClient{pages: [][]string{{"a"}}}
	response, err := queryStore(t, client).Search(t.Context(), &vectorstore.SearchRequest{
		Query:   "query",
		Options: vectorstore.SearchOptions{TopK: 5},
	})
	if err != nil {
		t.Fatalf("Search() = %v, want nil", err)
	}
	if len(response.Results) != 1 || client.calls != 1 {
		t.Fatalf("results = %d over %d calls, want 1 over 1", len(response.Results), client.calls)
	}
}

// A TopK past what a query can rank cannot be served, so it fails locally
// rather than as an opaque provider rejection.
func TestSearchRejectsTopKBeyondTheQueryLimit(t *testing.T) {
	t.Parallel()

	_, err := queryStore(t, &pagingQueryClient{}).Search(t.Context(), &vectorstore.SearchRequest{
		Query:   "query",
		Options: vectorstore.SearchOptions{TopK: MaxTopK + 1},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds the 10000 results") {
		t.Fatalf("Search() = %v, want a TopK ceiling error", err)
	}
}

// An empty page paired with a token would page forever.
func TestSearchRejectsEmptyPageWithToken(t *testing.T) {
	t.Parallel()

	client := &pagingQueryClient{pages: [][]string{{}, {"a"}}}
	_, err := queryStore(t, client).Search(t.Context(), &vectorstore.SearchRequest{
		Query:   "query",
		Options: vectorstore.SearchOptions{TopK: 3},
	})
	if err == nil || !strings.Contains(err.Error(), "empty page with a continuation token") {
		t.Fatalf("Search() = %v, want an empty-page error", err)
	}
}
