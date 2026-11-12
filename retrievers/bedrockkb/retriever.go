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
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/rag"
)

const (
	Provider                 = "BedrockKnowledgeBase"
	DefaultMaxResponseBytes  = int64(16 * 1024 * 1024)
	nativeRequestIDHeader    = "X-Amzn-Requestid"
	canonicalRequestIDHeader = "Request-Id"

	// SourceDocumentIDMetadataKey and SourceLocationMetadataKey reserve the
	// native provenance independently of the returned chunk identity.
	SourceDocumentIDMetadataKey = "bedrockkb_document_id"
	SourceLocationMetadataKey   = "bedrockkb_location"
)

var _ rag.Retriever = (*Retriever)(nil)

type Retriever struct {
	endpoint         string
	httpClient       *http.Client
	knowledgeBaseID  string
	userID           string
	maxResponseBytes int64
	pageLimit        int
	configuration    nativeConfiguration
}

// New performs no I/O. The host provisions the knowledge base and owns
// native retrieval policy; Retrieve is the only network operation.
func New(ctx context.Context, config Config) (*Retriever, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	ownedHTTP, err := httpclient.New(ctx, httpclient.Config{RejectRedirects: true, HTTPClient: config.HTTPClient, BaseURL: config.Endpoint})
	if err != nil {
		return nil, err
	}
	configuration, err := config.nativePolicy()
	if err != nil {
		return nil, err
	}
	store := &Retriever{
		endpoint: strings.TrimRight(config.Endpoint, "/"), httpClient: ownedHTTP, knowledgeBaseID: config.KnowledgeBaseID, userID: config.UserID,
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes), configuration: configuration, pageLimit: cmp.Or(config.PageLimit, DefaultPageLimit),
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

func (r retrievalResult) match(knowledgeBase string) (rag.Candidate, error) {
	if r.Score == nil || math.IsNaN(*r.Score) || math.IsInf(*r.Score, 0) {
		return rag.Candidate{}, errors.New("bedrockkb: missing or invalid native score")
	}
	if r.Content == nil || r.Content.Type != types.RetrievalResultContentTypeText || r.Content.Text == nil || *r.Content.Text == "" {
		return rag.Candidate{}, errors.New("bedrockkb: retrieval result has no native text content")
	}
	if err := r.Metadata.Validate(); err != nil {
		return rag.Candidate{}, err
	}
	for _, key := range []string{SourceDocumentIDMetadataKey, SourceLocationMetadataKey} {
		if _, exists := r.Metadata[key]; exists {
			return rag.Candidate{}, fmt.Errorf("bedrockkb: native metadata uses reserved provenance key %q", key)
		}
	}
	location, err := r.sourceLocation()
	if err != nil {
		return rag.Candidate{}, err
	}
	identity, err := jsonv2.Marshal(struct {
		KnowledgeBase string       `json:"knowledgeBase"`
		DocumentID    *string      `json:"documentId,omitzero"`
		Location      metadata.Map `json:"location,omitzero"`
		Text          string       `json:"text"`
		Metadata      metadata.Map `json:"metadata"`
	}{knowledgeBase, r.DocumentID, location, *r.Content.Text, r.Metadata})
	if err != nil {
		return rag.Candidate{}, err
	}
	// Native metadata is a source-file fact. Order its JSON keys recursively
	// without rounding the exact numbers; query-relative scores never enter ID.
	canonical := jsontext.Value(identity)
	if err = canonical.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return rag.Candidate{}, err
	}
	id := fmt.Sprintf("bedrockkb:%x", sha256.Sum256(canonical))
	facts := r.Metadata.Clone()
	if facts == nil {
		facts = make(metadata.Map)
	}
	if r.DocumentID != nil {
		if err = facts.Set(SourceDocumentIDMetadataKey, *r.DocumentID); err != nil {
			return rag.Candidate{}, err
		}
	}
	if location != nil {
		if err = facts.Set(SourceLocationMetadataKey, location); err != nil {
			return rag.Candidate{}, err
		}
	}
	doc := &document.Document{ID: id, Text: *r.Content.Text, Metadata: facts}
	candidate := rag.Candidate{Document: doc, Score: rag.Score(*r.Score)}
	return candidate, candidate.Validate()
}

// locationTypeMember names a location's type in the native location object.
const locationTypeMember = "type"

// locationIdentity names, for each native location type, the member holding
// that location and the property that identifies its source.
func locationIdentity(kind types.RetrievalResultLocationType) (member, property string, known bool) {
	switch kind {
	case types.RetrievalResultLocationTypeS3:
		return "s3Location", "uri", true
	case types.RetrievalResultLocationTypeWeb:
		return "webLocation", "url", true
	case types.RetrievalResultLocationTypeConfluence:
		return "confluenceLocation", "url", true
	case types.RetrievalResultLocationTypeSalesforce:
		return "salesforceLocation", "url", true
	case types.RetrievalResultLocationTypeSharepoint:
		return "sharePointLocation", "url", true
	case types.RetrievalResultLocationTypeKendra:
		return "kendraDocumentLocation", "uri", true
	case types.RetrievalResultLocationTypeCustom:
		return "customDocumentLocation", "id", true
	case types.RetrievalResultLocationTypeOnedrive:
		return "oneDriveLocation", "url", true
	case types.RetrievalResultLocationTypeGoogledrive:
		return "googleDriveLocation", "url", true
	default:
		return "", "", false
	}
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
	kind, _, err := location.Decode[string](locationTypeMember)
	if err != nil {
		return nil, err
	}
	if member, property, known := locationIdentity(types.RetrievalResultLocationType(kind)); known {
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

func (r *Retriever) Retrieve(ctx context.Context, query rag.Query) (rag.Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	config := r.configuration
	body := retrievalRequest{
		RetrievalQuery:         retrievalQuery{Text: query.Text()},
		RetrievalConfiguration: retrievalConfiguration{VectorSearchConfiguration: config},
	}
	if r.userID != "" {
		body.UserContext = &retrievalUserContext{UserID: r.userID}
	}
	ranked := make(rag.Candidates, 0, config.Count)
	remaining := r.maxResponseBytes
	seenTokens := make(map[string]struct{})
	for page := 0; ; page++ {
		if page >= r.pageLimit {
			return nil, errors.New("bedrockkb: retrieval page budget exhausted")
		}
		raw, retrieveErr := r.retrieve(ctx, body, remaining)
		if retrieveErr != nil {
			return nil, retrieveErr
		}
		var output struct {
			Results   []retrievalResult `json:"retrievalResults"`
			NextToken *string           `json:"nextToken"`
		}
		remaining -= int64(len(raw))
		if err := jsonv2.Unmarshal(raw, &output); err != nil {
			return nil, fmt.Errorf("bedrockkb: decode native retrieval: %w", err)
		}
		if output.Results == nil {
			return nil, errors.New("bedrockkb: native retrieval response is missing its result array")
		}
		for _, row := range output.Results {
			if config.Filter != nil {
				value, ok, err := row.Metadata.Decode[string](config.Filter.Equals.Key)
				if err != nil || !ok || value != config.Filter.Equals.Value {
					return nil, errors.New("bedrockkb: result outside bound metadata scope")
				}
			}
			match, matchErr := row.match(r.knowledgeBaseID)
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
		body.NextToken = *output.NextToken
	}
	slices.SortStableFunc(ranked, func(a, b rag.Candidate) int { return cmp.Compare(b.Score, a.Score) })
	return ranked[:min(len(ranked), config.Count)], nil
}

func (r *Retriever) retrieve(ctx context.Context, body retrievalRequest, remaining int64) ([]byte, error) {
	encoded, err := jsonv2.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint+"/knowledgebases/"+httpclient.PathSegment(r.knowledgeBaseID)+"/retrieve", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := r.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, remaining+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > remaining {
		return nil, fmt.Errorf("bedrockkb: response exceeds %d-byte limit", r.maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		headers := response.Header
		if requestID := headers.Get(nativeRequestIDHeader); requestID != "" {
			headers = headers.Clone()
			headers.Set(canonicalRequestIDHeader, requestID)
		}
		return nil, fmt.Errorf("bedrockkb: retrieve: %w", httpclient.NewResponseError(response.StatusCode, headers))
	}
	return raw, nil
}
