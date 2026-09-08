package bedrockkb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "BedrockKnowledgeBase"

// RetrieveClient is the Bedrock runtime surface the store uses. A
// [bedrockagentruntime.Client] satisfies it. Naming the single operation keeps
// the store's pagination observable without a live knowledge base.
type RetrieveClient interface {
	Retrieve(
		ctx context.Context,
		input *bedrockagentruntime.RetrieveInput,
		optFns ...func(*bedrockagentruntime.Options),
	) (*bedrockagentruntime.RetrieveOutput, error)
}

// StoreConfig contains configuration options for the AWS Bedrock
// Knowledge Base vector store. Bedrock manages document ingestion
// out of band (S3 data source + StartIngestionJob), so this store exposes only
// retrieval.
type StoreConfig struct {
	// Client is the bedrockagentruntime client. Required.
	Client RetrieveClient

	// KnowledgeBaseID identifies the knowledge base to query.
	// Required.
	KnowledgeBaseID string

	// RerankingConfiguration and ImplicitFilterConfiguration expose Bedrock's
	// provider-specific retrieval features without allowing them to override
	// SearchOptions.TopK or SearchOptions.Filter.
	RerankingConfiguration      *types.VectorSearchRerankingConfiguration
	ImplicitFilterConfiguration *types.ImplicitFilterConfiguration
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("bedrockkb: Client is required")
	}
	if s.KnowledgeBaseID == "" {
		return errors.New("bedrockkb: KnowledgeBaseID is required")
	}
	return nil
}

var _ vectorstore.Searcher = (*Store)(nil)

// Store is a searchable Bedrock Knowledge Base. Ingestion and deletion are
// intentionally absent because the runtime API cannot perform them.
type Store struct {
	client                      RetrieveClient
	knowledgeBaseID             string
	rerankingConfiguration      *types.VectorSearchRerankingConfiguration
	implicitFilterConfiguration *types.ImplicitFilterConfiguration
}

// NewStore performs no I/O: the knowledge base is provisioned out of band and
// the only surface this store depends on is Retrieve, which cannot describe a
// knowledge base without running a paid query. Confirming one exists would
// mean taking a second, control-plane client the store has no other use for,
// so a wrong KnowledgeBaseID surfaces as Bedrock's own error on the first
// search.
//
// The context is still taken, because every store in this family is
// constructed the same way and a caller should not have to remember which
// backend happens to be checkable.
func NewStore(_ context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Store{
		client:                      config.Client,
		knowledgeBaseID:             config.KnowledgeBaseID,
		rerankingConfiguration:      config.RerankingConfiguration,
		implicitFilterConfiguration: config.ImplicitFilterConfiguration,
	}, nil
}

// Search runs the Bedrock Knowledge Base Retrieve API.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("bedrockkb.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, fmt.Errorf("bedrockkb.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	var results []types.KnowledgeBaseRetrievalResult
	results, err = s.retrieve(ctx, req)
	if err != nil {
		return nil, err
	}

	docs = make([]*vectorstore.SearchResult, 0, len(results))
	for _, r := range results {
		match, err := toMatch(r)
		if err != nil {
			return nil, err
		}
		if match.Score < req.Options.MinScore {
			continue
		}
		docs = append(docs, match)
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

// maxResultsPerPage is Bedrock's documented ceiling for
// KnowledgeBaseVectorSearchConfiguration.NumberOfResults. A request for more
// results than this is not a caller error, it is a request for more than one
// page.
const maxResultsPerPage = 100

// retrieve collects the requested number of results, following Bedrock's
// nextToken until it has them or the knowledge base runs out.
//
// One Retrieve response is not the whole answer twice over: NumberOfResults
// caps at maxResultsPerPage, and Bedrock returns a nextToken whenever there are
// "more results than can fit in the response", so even a page below that cap
// can come back short. Only a missing token establishes that no further results
// exist. NumberOfResults stays the same on every call because the token
// continues the query the first call started.
func (s *Store) retrieve(
	ctx context.Context,
	req *vectorstore.SearchRequest,
) ([]types.KnowledgeBaseRetrievalResult, error) {
	limit := req.Options.ResultLimit()
	vectorCfg, err := s.vectorSearchConfig(req, min(limit, maxResultsPerPage))
	if err != nil {
		return nil, err
	}

	results := make([]types.KnowledgeBaseRetrievalResult, 0, limit)
	var token *string
	for {
		output, retrieveErr := s.client.Retrieve(ctx, &bedrockagentruntime.RetrieveInput{
			KnowledgeBaseId: aws.String(s.knowledgeBaseID),
			RetrievalQuery:  &types.KnowledgeBaseQuery{Text: aws.String(req.Query)},
			RetrievalConfiguration: &types.KnowledgeBaseRetrievalConfiguration{
				VectorSearchConfiguration: vectorCfg,
			},
			NextToken: token,
		})
		if retrieveErr != nil {
			return nil, fmt.Errorf("bedrockkb: retrieve: %w", retrieveErr)
		}
		results = append(results, output.RetrievalResults...)
		if len(results) >= limit {
			return results[:limit], nil
		}
		if output.NextToken == nil || *output.NextToken == "" {
			return results, nil
		}
		if len(output.RetrievalResults) == 0 {
			return nil, errors.New("bedrockkb: retrieve returned an empty page with a continuation token")
		}
		token = output.NextToken
	}
}

// vectorSearchConfig builds the per-call vector search configuration, layering
// caller-supplied overrides on top of the request defaults.
func (s *Store) vectorSearchConfig(
	req *vectorstore.SearchRequest,
	resultsPerPage int,
) (*types.KnowledgeBaseVectorSearchConfiguration, error) {
	perPage := int32(resultsPerPage)
	searchType := types.SearchTypeSemantic
	if req.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		searchType = types.SearchTypeHybrid
	}
	config := &types.KnowledgeBaseVectorSearchConfiguration{
		NumberOfResults:             &perPage,
		OverrideSearchType:          searchType,
		RerankingConfiguration:      s.rerankingConfiguration,
		ImplicitFilterConfiguration: s.implicitFilterConfiguration,
	}

	if req.Options.Filter != nil {
		visitor := newVisitor()
		if err := req.Options.Filter.Accept(visitor); err != nil {
			return nil, fmt.Errorf("bedrockkb.Store.Search: compile metadata filter: %w", err)
		}
		config.Filter = visitor.snapshot()
	}
	return config, nil
}

// toMatch converts a Bedrock retrieval result into a Scope match.
func toMatch(r types.KnowledgeBaseRetrievalResult) (*vectorstore.SearchResult, error) {
	doc := &document.Document{}
	if r.Score == nil {
		return nil, errors.New("bedrockkb: retrieval result is missing score")
	}
	score := vectorstore.ScoreFromValue(*r.Score)
	if r.Content != nil && r.Content.Text != nil {
		doc.Text = *r.Content.Text
	}
	if doc.Text == "" {
		return nil, errors.New("bedrockkb: retrieval result has no text content")
	}

	if len(r.Metadata) > 0 {
		meta := make(map[string]any, len(r.Metadata))
		for k, v := range r.Metadata {
			var decoded any
			if err := v.UnmarshalSmithyDocument(&decoded); err != nil {
				return nil, fmt.Errorf("bedrockkb: decode metadata key %s: %w", k, err)
			}
			meta[k] = decoded
		}
		var err error
		doc.Metadata, err = metadata.FromValues(meta)
		if err != nil {
			return nil, fmt.Errorf("bedrockkb: convert metadata: %w", err)
		}
	}

	if r.DocumentId != nil {
		doc.ID = *r.DocumentId
	}
	// Some knowledge-base source types do not return DocumentId. Their
	// provider-native location is still stable and losslessly identifies the
	// retrieval source.
	if doc.ID == "" && r.Location != nil {
		location, err := json.Marshal(r.Location)
		if err != nil {
			return nil, fmt.Errorf("bedrockkb: encode result location: %w", err)
		}
		if string(location) != "{}" && string(location) != "null" {
			doc.ID = string(location)
		}
	}
	if doc.ID == "" {
		return nil, errors.New("bedrockkb: retrieval result has no stable document identity")
	}
	return &vectorstore.SearchResult{Document: doc, Score: score}, nil
}
