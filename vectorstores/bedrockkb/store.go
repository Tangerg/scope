package bedrockkb

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/json/jsontext"
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

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider                = "BedrockKnowledgeBase"
	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)
	maximumResults          = 100

	// SourceDocumentIDMetadataKey and SourceLocationMetadataKey reserve the
	// native provenance independently of the returned chunk identity.
	SourceDocumentIDMetadataKey = "bedrockkb_document_id"
	SourceLocationMetadataKey   = "bedrockkb_location"
)

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
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	ownedHTTP, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: config.Endpoint})
	if err != nil {
		return nil, err
	}
	configuration, err := config.nativePolicy()
	if err != nil {
		return nil, err
	}
	store := &Store{
		endpoint: strings.TrimRight(config.Endpoint, "/"), httpClient: ownedHTTP, knowledgeBaseID: config.KnowledgeBaseID,
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes), configuration: configuration,
	}
	return store, nil
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
	if err := r.Metadata.Validate(); err != nil {
		return rankedResult{}, err
	}
	for _, key := range []string{SourceDocumentIDMetadataKey, SourceLocationMetadataKey} {
		if _, exists := r.Metadata[key]; exists {
			return rankedResult{}, fmt.Errorf("bedrockkb: native metadata uses reserved provenance key %q", key)
		}
	}
	location, err := r.sourceLocation()
	if err != nil {
		return rankedResult{}, err
	}
	identity, err := jsonv2.Marshal(struct {
		DocumentID *string      `json:"documentId,omitzero"`
		Location   metadata.Map `json:"location,omitzero"`
		Text       string       `json:"text"`
		Metadata   metadata.Map `json:"metadata"`
	}{r.DocumentID, location, *r.Content.Text, r.Metadata})
	if err != nil {
		return rankedResult{}, err
	}
	// Native metadata is a source-file fact. Order its JSON keys recursively
	// without rounding the exact numbers; query-relative scores never enter ID.
	canonical := jsontext.Value(identity)
	if err = canonical.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return rankedResult{}, err
	}
	id := fmt.Sprintf("bedrockkb:%x", sha256.Sum256(canonical))
	facts := r.Metadata.Clone()
	if facts == nil {
		facts = make(metadata.Map)
	}
	if r.DocumentID != nil {
		if err = facts.Set(SourceDocumentIDMetadataKey, *r.DocumentID); err != nil {
			return rankedResult{}, err
		}
	}
	if location != nil {
		if err = facts.Set(SourceLocationMetadataKey, location); err != nil {
			return rankedResult{}, err
		}
	}
	doc := &document.Document{ID: id, Text: *r.Content.Text, Metadata: facts}
	result, err := vectorstore.NewSearchResult(doc, vectorstore.ScoreFromValue(*r.Score))
	if err != nil {
		return rankedResult{}, err
	}
	return rankedResult{result: result, rawScore: *r.Score}, nil
}

func (r retrievalResult) sourceLocation() (metadata.Map, error) {
	var location metadata.Map
	if len(r.Location) != 0 {
		if err := location.UnmarshalJSON(r.Location); err != nil {
			return nil, err
		}
	}
	if r.DocumentID != nil {
		if strings.TrimSpace(*r.DocumentID) == "" {
			return nil, errors.New("bedrockkb: native document identity is empty")
		}
		return location, nil
	}
	kind, _, err := location.Decode[string]("type")
	if err != nil {
		return nil, err
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
			return nil, decodeErr
		}
		value, exists, decodeErr := source.Decode[string](property)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if present && exists && strings.TrimSpace(value) != "" {
			return location, nil
		}
	}
	return nil, errors.New("bedrockkb: retrieval result has no stable native source identity")
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
