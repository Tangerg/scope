package bedrockkb

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/Tangerg/scope/core/vectorstore"
)

// pagedKnowledgeBase answers Retrieve from a scripted list of pages and records
// what each call asked for.
type pagedKnowledgeBase struct {
	pages   [][]string
	tokens  []*string
	perPage []int32
}

func (p *pagedKnowledgeBase) Retrieve(
	_ context.Context,
	input *bedrockagentruntime.RetrieveInput,
	_ ...func(*bedrockagentruntime.Options),
) (*bedrockagentruntime.RetrieveOutput, error) {
	p.tokens = append(p.tokens, input.NextToken)
	p.perPage = append(
		p.perPage,
		*input.RetrievalConfiguration.VectorSearchConfiguration.NumberOfResults,
	)

	index := len(p.tokens) - 1
	if index >= len(p.pages) {
		return nil, fmt.Errorf("unexpected Retrieve call %d", index+1)
	}
	output := &bedrockagentruntime.RetrieveOutput{}
	for _, id := range p.pages[index] {
		output.RetrievalResults = append(output.RetrievalResults, types.KnowledgeBaseRetrievalResult{
			Content:    &types.RetrievalResultContent{Text: aws.String("text of " + id)},
			DocumentId: aws.String(id),
			Score:      aws.Float64(0.5),
		})
	}
	if index+1 < len(p.pages) {
		output.NextToken = aws.String(fmt.Sprintf("token-%d", index+1))
	}
	return output, nil
}

func retrieveStore(client RetrieveClient) *Store {
	return &Store{client: client, knowledgeBaseID: "KB12345678"}
}

func searchIDs(t *testing.T, store *Store, topK int) []string {
	t.Helper()
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{
		Query:   "query",
		Options: vectorstore.SearchOptions{TopK: topK},
	})
	if err != nil {
		t.Fatalf("Search() = %v, want nil", err)
	}
	ids := make([]string, 0, len(response.Results))
	for _, result := range response.Results {
		ids = append(ids, result.Document.ID)
	}
	return ids
}

// Bedrock returns a nextToken "if there are more results than can fit in the
// response", so a page holding fewer results than were asked for does not prove
// the knowledge base has no more.
func TestSearchFollowsContinuationTokenAfterShortPage(t *testing.T) {
	t.Parallel()

	client := &pagedKnowledgeBase{pages: [][]string{{"one"}, {"two"}, {"three"}}}
	got := searchIDs(t, retrieveStore(client), 3)

	if want := []string{"one", "two", "three"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("result ids = %v, want %v", got, want)
	}
	if len(client.tokens) != 3 {
		t.Fatalf("Retrieve calls = %d, want 3", len(client.tokens))
	}
	if client.tokens[0] != nil {
		t.Fatalf("first call NextToken = %v, want nil", *client.tokens[0])
	}
	for index, token := range client.tokens[1:] {
		if token == nil || *token != fmt.Sprintf("token-%d", index+1) {
			t.Fatalf("call %d NextToken = %v, want token-%d", index+2, token, index+1)
		}
	}
}

// A missing token is the only evidence that the knowledge base is exhausted,
// and an exhausted knowledge base may hold fewer results than were requested.
func TestSearchStopsWhenNoContinuationTokenRemains(t *testing.T) {
	t.Parallel()

	client := &pagedKnowledgeBase{pages: [][]string{{"one", "two"}}}
	if got := searchIDs(t, retrieveStore(client), 5); len(got) != 2 {
		t.Fatalf("result ids = %v, want two results", got)
	}
	if len(client.tokens) != 1 {
		t.Fatalf("Retrieve calls = %d, want 1", len(client.tokens))
	}
}

// NumberOfResults tops out at 100, so a larger TopK is a request for more than
// one page rather than an input Bedrock will reject.
func TestSearchCapsPageSizeAtProviderMaximum(t *testing.T) {
	t.Parallel()

	first := make([]string, maxResultsPerPage)
	for index := range first {
		first[index] = fmt.Sprintf("id-%d", index)
	}
	client := &pagedKnowledgeBase{pages: [][]string{first, {"extra"}}}

	if got := searchIDs(t, retrieveStore(client), maxResultsPerPage+1); len(got) != maxResultsPerPage+1 {
		t.Fatalf("result count = %d, want %d", len(got), maxResultsPerPage+1)
	}
	for index, perPage := range client.perPage {
		if perPage != maxResultsPerPage {
			t.Fatalf("call %d NumberOfResults = %d, want %d", index+1, perPage, maxResultsPerPage)
		}
	}
}

// A page may overshoot the request once TopK is not a multiple of the page
// size; the response must still honor TopK.
func TestSearchTrimsToRequestedResultCount(t *testing.T) {
	t.Parallel()

	client := &pagedKnowledgeBase{pages: [][]string{{"one", "two", "three"}, {"four"}}}
	got := searchIDs(t, retrieveStore(client), 2)

	if want := []string{"one", "two"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("result ids = %v, want %v", got, want)
	}
	if len(client.tokens) != 1 {
		t.Fatalf("Retrieve calls = %d, want 1", len(client.tokens))
	}
}

// An empty page paired with a token would page forever.
func TestSearchRejectsEmptyPageWithContinuationToken(t *testing.T) {
	t.Parallel()

	client := &pagedKnowledgeBase{pages: [][]string{{}, {"one"}}}
	_, err := retrieveStore(client).Search(t.Context(), &vectorstore.SearchRequest{
		Query:   "query",
		Options: vectorstore.SearchOptions{TopK: 3},
	})
	if err == nil || !strings.Contains(err.Error(), "empty page with a continuation token") {
		t.Fatalf("Search() = %v, want an empty-page error", err)
	}
}

var _ RetrieveClient = (*bedrockagentruntime.Client)(nil)
