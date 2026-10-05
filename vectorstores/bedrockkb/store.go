package bedrockkb

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider                = "BedrockKnowledgeBase"
	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)
	maximumResults          = 100
)

// StoreConfig binds the native Retrieve REST boundary. The host owns HTTP
// authentication, retries, timeouts and lifecycle; ingestion stays in Bedrock.
type StoreConfig struct {
	Endpoint                       string
	HTTPClient                     *http.Client
	KnowledgeBaseID                string
	MaxResponseBytes               int64
	RerankingModelConfiguration    *types.VectorSearchBedrockRerankingModelConfiguration
	RerankingMetadataConfiguration *types.MetadataConfigurationForReranking
	ImplicitFilterConfiguration    *types.ImplicitFilterConfiguration
}

func (s StoreConfig) Validate() error {
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.Trim(endpoint.Path, "/") != "" {
		return errors.New("bedrockkb: Endpoint must be an HTTP service origin")
	}
	if s.HTTPClient == nil {
		return errors.New("bedrockkb: HTTPClient is required")
	}
	if strings.TrimSpace(s.KnowledgeBaseID) == "" {
		return errors.New("bedrockkb: KnowledgeBaseID is required")
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("bedrockkb: MaxResponseBytes must be nonnegative and below MaxInt64")
	}
	if s.RerankingMetadataConfiguration != nil && s.RerankingModelConfiguration == nil {
		return errors.New("bedrockkb: reranking metadata requires a reranking model")
	}
	return nil
}

var _ vectorstore.Searcher = (*Store)(nil)

type Store struct {
	endpoint         string
	httpClient       *http.Client
	knowledgeBaseID  string
	maxResponseBytes int64
	configuration    nativeConfiguration
}

// NewStore performs no I/O. The host provisions the knowledge base and owns
// native retrieval policy; Search is the only network operation.
func NewStore(_ context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	policy, err := config.nativePolicy()
	if err != nil {
		return nil, err
	}
	encoded, err := jsonv2.Marshal(policy)
	if err != nil {
		return nil, err
	}
	var configuration nativeConfiguration
	if err = jsonv2.Unmarshal(encoded, &configuration); err != nil {
		return nil, err
	}
	store := &Store{
		endpoint: strings.TrimRight(config.Endpoint, "/"), httpClient: config.HTTPClient, knowledgeBaseID: config.KnowledgeBaseID,
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes), configuration: configuration,
	}
	return store, nil
}

func (s StoreConfig) nativePolicy() (map[string]any, error) {
	config := make(map[string]any)
	if s.RerankingModelConfiguration != nil {
		if s.RerankingModelConfiguration.ModelArn == nil || strings.TrimSpace(*s.RerankingModelConfiguration.ModelArn) == "" {
			return nil, errors.New("bedrockkb: reranking model ARN is required")
		}
		fields := make(metadata.Map, len(s.RerankingModelConfiguration.AdditionalModelRequestFields))
		for key, value := range s.RerankingModelConfiguration.AdditionalModelRequestFields {
			if lo.IsNil(value) {
				return nil, fmt.Errorf("bedrockkb: nil reranking model field %q", key)
			}
			raw, err := value.MarshalSmithyDocument()
			if err != nil {
				return nil, err
			}
			fields[key] = raw
		}
		if err := fields.Validate(); err != nil {
			return nil, err
		}
		model := map[string]any{"modelArn": *s.RerankingModelConfiguration.ModelArn}
		if len(fields) != 0 {
			model["additionalModelRequestFields"] = fields
		}
		reranking := map[string]any{"modelConfiguration": model}
		if s.RerankingMetadataConfiguration != nil {
			configured := s.RerankingMetadataConfiguration
			selection := map[string]any{"selectionMode": configured.SelectionMode}
			switch configured.SelectionMode {
			case types.RerankingMetadataSelectionModeAll:
				if !lo.IsNil(configured.SelectiveModeConfiguration) {
					return nil, errors.New("bedrockkb: ALL reranking metadata cannot carry selective fields")
				}
			case types.RerankingMetadataSelectionModeSelective:
				var name string
				var fields []types.FieldForReranking
				switch subset := configured.SelectiveModeConfiguration.(type) {
				case *types.RerankingMetadataSelectiveModeConfigurationMemberFieldsToInclude:
					if subset != nil {
						name = "fieldsToInclude"
						fields = subset.Value
					}
				case *types.RerankingMetadataSelectiveModeConfigurationMemberFieldsToExclude:
					if subset != nil {
						name = "fieldsToExclude"
						fields = subset.Value
					}
				}
				if name == "" || len(fields) == 0 {
					return nil, errors.New("bedrockkb: SELECTIVE reranking metadata requires native fields")
				}
				projected := make([]map[string]any, len(fields))
				for i, field := range fields {
					if field.FieldName == nil || *field.FieldName == "" {
						return nil, errors.New("bedrockkb: reranking metadata field name is required")
					}
					projected[i] = map[string]any{"fieldName": *field.FieldName}
				}
				selection["selectiveModeConfiguration"] = map[string]any{name: projected}
			default:
				return nil, errors.New("bedrockkb: unsupported native reranking metadata selection mode")
			}
			reranking["metadataConfiguration"] = selection
		}
		config["rerankingConfiguration"] = map[string]any{"type": types.VectorSearchRerankingConfigurationTypeBedrockRerankingModel, "bedrockRerankingConfiguration": reranking}
	}
	if s.ImplicitFilterConfiguration != nil {
		if s.ImplicitFilterConfiguration.ModelArn == nil || *s.ImplicitFilterConfiguration.ModelArn == "" {
			return nil, errors.New("bedrockkb: implicit filter model ARN is required")
		}
		attributes := make([]map[string]any, len(s.ImplicitFilterConfiguration.MetadataAttributes))
		for i, attribute := range s.ImplicitFilterConfiguration.MetadataAttributes {
			attributes[i] = map[string]any{"key": attribute.Key, "type": attribute.Type, "description": attribute.Description}
		}
		config["implicitFilterConfiguration"] = map[string]any{"modelArn": *s.ImplicitFilterConfiguration.ModelArn, "metadataAttributes": attributes}
	}
	return config, nil
}

type nativeConfiguration struct {
	Count     int              `json:"numberOfResults"`
	Mode      types.SearchType `json:"overrideSearchType"`
	Reranking *struct {
		Type    types.VectorSearchRerankingConfigurationType `json:"type"`
		Bedrock struct {
			Model    json.RawMessage `json:"modelConfiguration"`
			Metadata json.RawMessage `json:"metadataConfiguration,omitzero"`
			Count    int             `json:"numberOfRerankedResults"`
		} `json:"bedrockRerankingConfiguration"`
	} `json:"rerankingConfiguration,omitzero"`
	Implicit json.RawMessage `json:"implicitFilterConfiguration,omitzero"`
}

func (n nativeConfiguration) forOptions(options vectorstore.SearchOptions) nativeConfiguration {
	n.Count = options.ResultLimit()
	n.Mode = types.SearchTypeSemantic
	if options.EffectiveMode() == vectorstore.SearchModeHybrid {
		n.Mode = types.SearchTypeHybrid
	}
	if n.Reranking != nil {
		reranking := *n.Reranking
		reranking.Bedrock.Count = options.ResultLimit()
		n.Reranking = &reranking
	}
	return n
}

type retrievalResult struct {
	DocumentID *string `json:"documentId"`
	Content    *struct {
		Type types.RetrievalResultContentType `json:"type"`
		Text *string                          `json:"text"`
	} `json:"content"`
	Location json.RawMessage `json:"location"`
	Metadata metadata.Map    `json:"metadata"`
	Score    *float64        `json:"score"`
}

type rankedResult struct {
	result   *vectorstore.SearchResult
	rawScore float64
}

func (r retrievalResult) match() (rankedResult, error) {
	if r.Score == nil || math.IsNaN(*r.Score) || math.IsInf(*r.Score, 0) {
		return rankedResult{}, errors.New("bedrockkb: missing or invalid native score")
	}
	if r.Content == nil || r.Content.Type != types.RetrievalResultContentTypeText || r.Content.Text == nil || *r.Content.Text == "" {
		return rankedResult{}, errors.New("bedrockkb: retrieval result has no native text content")
	}
	id := ""
	if r.DocumentID != nil {
		id = *r.DocumentID
	}
	if id == "" && len(r.Location) != 0 {
		var location metadata.Map
		if err := location.UnmarshalJSON(r.Location); err != nil {
			return rankedResult{}, err
		}
		kind, _, err := location.Decode[string]("type")
		if err != nil {
			return rankedResult{}, err
		}
		member, property := "", ""
		switch types.RetrievalResultLocationType(kind) {
		case types.RetrievalResultLocationTypeS3:
			member, property = "s3Location", "uri"
		case types.RetrievalResultLocationTypeWeb:
			member, property = "webLocation", "url"
		case types.RetrievalResultLocationTypeConfluence:
			member, property = "confluenceLocation", "url"
		case types.RetrievalResultLocationTypeSalesforce:
			member, property = "salesforceLocation", "url"
		case types.RetrievalResultLocationTypeSharepoint:
			member, property = "sharePointLocation", "url"
		case types.RetrievalResultLocationTypeKendra:
			member, property = "kendraDocumentLocation", "uri"
		case types.RetrievalResultLocationTypeCustom:
			member, property = "customDocumentLocation", "id"
		case types.RetrievalResultLocationTypeOnedrive:
			member, property = "oneDriveLocation", "url"
		case types.RetrievalResultLocationTypeGoogledrive:
			member, property = "googleDriveLocation", "url"
		}
		if member != "" {
			source, present, decodeErr := location.Decode[metadata.Map](member)
			if decodeErr != nil {
				return rankedResult{}, decodeErr
			}
			value, exists, decodeErr := source.Decode[string](property)
			if decodeErr != nil {
				return rankedResult{}, decodeErr
			}
			if present && exists && strings.TrimSpace(value) != "" {
				encoded, encodeErr := jsonv2.Marshal(map[string]any{"type": kind, member: map[string]any{property: value}}, jsonv2.Deterministic(true))
				if encodeErr != nil {
					return rankedResult{}, encodeErr
				}
				id = string(encoded)
			}
		}
	}
	if id == "" {
		return rankedResult{}, errors.New("bedrockkb: retrieval result has no stable native source identity")
	}
	doc := &document.Document{ID: id, Text: *r.Content.Text, Metadata: r.Metadata}
	result, err := vectorstore.NewSearchResult(doc, vectorstore.ScoreFromValue(*r.Score))
	if err != nil {
		return rankedResult{}, err
	}
	return rankedResult{result: result, rawScore: *r.Score}, nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, err
	}
	if request.Options.Filter != nil {
		return nil, fmt.Errorf("bedrockkb: Core predicates cannot be evaluated over the complete knowledge base: %w", errors.ErrUnsupported)
	}
	if request.Options.ResultLimit() > maximumResults {
		return nil, fmt.Errorf("bedrockkb: %w: native numberOfResults cannot exceed %d", vectorstore.ErrInvalidOptions, maximumResults)
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	config := s.configuration.forOptions(request.Options)
	body := map[string]any{"retrievalQuery": map[string]any{"text": request.Query}, "retrievalConfiguration": map[string]any{"vectorSearchConfiguration": config}}
	ranked := make([]rankedResult, 0, request.Options.ResultLimit())
	seenTokens := make(map[string]struct{})
	for {
		raw, retrieveErr := s.retrieve(ctx, body)
		if retrieveErr != nil {
			return nil, retrieveErr
		}
		var output struct {
			Results   []retrievalResult `json:"retrievalResults"`
			NextToken *string           `json:"nextToken"`
		}
		if err = jsonv2.Unmarshal(raw, &output); err != nil {
			return nil, fmt.Errorf("bedrockkb: decode native retrieval: %w", err)
		}
		if output.Results == nil {
			return nil, errors.New("bedrockkb: native retrieval response is missing its result array")
		}
		for _, row := range output.Results {
			match, matchErr := row.match()
			if matchErr != nil {
				return nil, matchErr
			}
			ranked = append(ranked, match)
		}
		if output.NextToken == nil || *output.NextToken == "" {
			break
		}
		if _, repeated := seenTokens[*output.NextToken]; repeated {
			return nil, errors.New("bedrockkb: native continuation token repeated")
		}
		seenTokens[*output.NextToken] = struct{}{}
		body["nextToken"] = *output.NextToken
	}
	slices.SortStableFunc(ranked, func(left, right rankedResult) int { return cmp.Compare(right.rawScore, left.rawScore) })
	results := make([]*vectorstore.SearchResult, 0, min(len(ranked), request.Options.ResultLimit()))
	for _, row := range ranked {
		if row.result.Score < request.Options.MinScore {
			continue
		}
		results = append(results, row.result)
		if len(results) == request.Options.ResultLimit() {
			break
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) retrieve(ctx context.Context, body any) ([]byte, error) {
	encoded, err := jsonv2.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint+"/knowledgebases/"+url.PathEscape(s.knowledgeBaseID)+"/retrieve", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, s.maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > s.maxResponseBytes {
		return nil, fmt.Errorf("bedrockkb: response exceeds %d-byte limit", s.maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("bedrockkb: native status=%d body=%s", response.StatusCode, raw)
	}
	return raw, nil
}
