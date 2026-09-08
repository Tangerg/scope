package s3vectors

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// listedVectors answers ListVectors with the scripted pages and records every
// deleted key. Unused operations stay unimplemented so a call to one fails the
// test rather than passing silently.
type listedVectors struct {
	VectorClient
	pages   [][]types.ListOutputVector
	tokens  []string
	calls   int
	deleted []string
	listErr error
}

func (l *listedVectors) ListVectors(
	ctx context.Context,
	input *s3vectors.ListVectorsInput,
	_ ...func(*s3vectors.Options),
) (*s3vectors.ListVectorsOutput, error) {
	if l.listErr != nil {
		return nil, l.listErr
	}
	if l.calls > 0 && (input.NextToken == nil || *input.NextToken != l.tokens[l.calls-1]) {
		l.calls++
		return nil, errors.New("continuation token was not echoed back")
	}
	page := l.pages[l.calls]
	output := &s3vectors.ListVectorsOutput{Vectors: page}
	if l.calls < len(l.tokens) {
		output.NextToken = aws.String(l.tokens[l.calls])
	}
	l.calls++
	return output, nil
}

func (l *listedVectors) DeleteVectors(
	ctx context.Context,
	input *s3vectors.DeleteVectorsInput,
	_ ...func(*s3vectors.Options),
) (*s3vectors.DeleteVectorsOutput, error) {
	l.deleted = append(l.deleted, input.Keys...)
	return &s3vectors.DeleteVectorsOutput{}, nil
}

func listed(key string, values map[string]any) types.ListOutputVector {
	document := map[string]any{contentMetaKey: "text of " + key}
	for name, value := range values {
		document[name] = value
	}
	return types.ListOutputVector{Key: aws.String(key), Metadata: s3vdoc.NewLazyDocument(document)}
}

func deleteWhereStore(client VectorClient) *Store {
	return &Store{vectorBucketName: "bucket", indexName: "documents", client: client}
}

// QueryVectors is an approximate search that answers with up to topK
// candidates, so it cannot enumerate a filter. Deletion lists the index
// exhaustively and decides membership locally, following the continuation token
// until the listing reports completion.
func TestDeleteWhereEnumeratesEveryListedVector(t *testing.T) {
	t.Parallel()

	client := &listedVectors{
		pages: [][]types.ListOutputVector{
			{listed("a", map[string]any{"tenant": "acme"}), listed("b", map[string]any{"tenant": "other"})},
			// An empty page mid-listing must not end the walk.
			{},
			{listed("c", map[string]any{"tenant": "acme"})},
		},
		tokens: []string{"page-2", "page-3"},
	}
	predicate, err := filter.Parse(`tenant == 'acme'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteWhereStore(client).DeleteWhere(t.Context(), predicate); err != nil {
		t.Fatalf("DeleteWhere() = %v, want nil", err)
	}
	if !slices.Equal(client.deleted, []string{"a", "c"}) {
		t.Fatalf("deleted = %v, want [a c]", client.deleted)
	}
	if client.calls != 3 {
		t.Fatalf("ListVectors calls = %d, want 3", client.calls)
	}
}

func TestDeleteWhereDeletesNothingWhenNoVectorMatches(t *testing.T) {
	t.Parallel()

	client := &listedVectors{
		pages:  [][]types.ListOutputVector{{listed("a", map[string]any{"tenant": "other"})}},
		tokens: nil,
	}
	predicate, err := filter.Parse(`tenant == 'acme'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteWhereStore(client).DeleteWhere(t.Context(), predicate); err != nil {
		t.Fatal(err)
	}
	if len(client.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", client.deleted)
	}
}

// A listing failure must not be reported as an empty match set, because the
// caller would read that as "the filter matched nothing".
func TestDeleteWhereReportsListingFailure(t *testing.T) {
	t.Parallel()

	client := &listedVectors{listErr: errors.New("throttled")}
	predicate, err := filter.Parse(`tenant == 'acme'`)
	if err != nil {
		t.Fatal(err)
	}
	err = deleteWhereStore(client).DeleteWhere(t.Context(), predicate)
	if err == nil || !strings.Contains(err.Error(), "s3vectors: list vectors: throttled") {
		t.Fatalf("DeleteWhere() = %v, want a listing failure", err)
	}
	if len(client.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", client.deleted)
	}
}

func TestDeleteWhereRejectsListedVectorWithoutKey(t *testing.T) {
	t.Parallel()

	client := &listedVectors{pages: [][]types.ListOutputVector{{{}}}}
	predicate, err := filter.Parse(`tenant == 'acme'`)
	if err != nil {
		t.Fatal(err)
	}
	err = deleteWhereStore(client).DeleteWhere(t.Context(), predicate)
	if err == nil || !strings.Contains(err.Error(), "listed vector[0] is missing key") {
		t.Fatalf("DeleteWhere() = %v, want a missing-key error", err)
	}
}
